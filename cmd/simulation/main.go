package main

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"sync"
	"time"

	"offlinepay/internal/api"
	"offlinepay/internal/chaos"
	"offlinepay/internal/cluster"
	"offlinepay/internal/config"
	"offlinepay/internal/crypto"
	"offlinepay/internal/db"
	"offlinepay/internal/domain"
	"offlinepay/internal/eventbus"
	"offlinepay/internal/identity"
	"offlinepay/internal/intent"
	"offlinepay/internal/outbox"
	"offlinepay/internal/reconciliation"
	"offlinepay/internal/relay"
	"offlinepay/internal/repository"
	"offlinepay/internal/risk"
	"offlinepay/internal/settlement"
	"offlinepay/internal/token"
	"offlinepay/internal/validator"
)

// Simulated client node representing an offline payer
type ClientNode struct {
	id         string
	devicePriv *ecdsa.PrivateKey
	publicKey  string
}

// Simulated network transport with packet loss and delay
type MockTransport struct {
	lossRate        float64
	latencyMin      time.Duration
	latencyMax      time.Duration
	packetDelivered int
	packetDropped   int
	mu              sync.Mutex
}

func (t *MockTransport) Send(ctx context.Context, action func() error) error {
	t.mu.Lock()
	// Simulate packet loss
	if rand.Float64() < t.lossRate {
		t.packetDropped++
		t.mu.Unlock()
		return fmt.Errorf("network packet lost in proximity transit (simulated loss)")
	}
	t.packetDelivered++
	t.mu.Unlock()

	// Simulate network delay
	delay := t.latencyMin
	if t.latencyMax > t.latencyMin {
		delay += time.Duration(rand.Int63n(int64(t.latencyMax - t.latencyMin)))
	}
	time.Sleep(delay)

	return action()
}

// ByzantineRelayClient wraps a SettlementClient to simulate bad relay nodes
type ByzantineRelayClient struct {
	client   relay.SettlementClient
	behavior string
}

func (b *ByzantineRelayClient) Settle(ctx context.Context, env *domain.EncryptedEnvelope, hopCount int) (string, error) {
	switch b.behavior {
	case "spam":
		var status string
		var finalErr error
		var wg sync.WaitGroup
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				st, err := b.client.Settle(ctx, env, hopCount)
				if err != nil {
					finalErr = err
				} else {
					status = st
				}
			}()
		}
		wg.Wait()
		return status, finalErr

	case "delayed":
		time.Sleep(200 * time.Millisecond)
		return b.client.Settle(ctx, env, hopCount)

	case "lazy":
		if rand.Float64() < 0.5 {
			return "", errors.New("lazy relay simulated packet drop")
		}
		return b.client.Settle(ctx, env, hopCount)

	case "byzantine":
		mutated := *env
		if len(mutated.Ciphertext) > 4 {
			mutated.Ciphertext = mutated.Ciphertext[:len(mutated.Ciphertext)-4] + "AAAA"
		} else {
			mutated.Ciphertext = "invalidciphertext"
		}
		return b.client.Settle(ctx, &mutated, hopCount)

	default:
		return b.client.Settle(ctx, env, hopCount)
	}
}

func main() {
	// 1. Setup Logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	slog.Info("==========================================================")
	slog.Info("   OFFLINEPAY DISTRIBUTED CHAOS SIMULATION RUNNER          ")
	slog.Info("==========================================================")

	cfg := config.LoadConfig()

	// 2. Connect to Database (requires a running postgres database)
	database, err := db.Connect(cfg.DBURL)
	if err != nil {
		slog.Error("Database connection failed. Is PostgreSQL running?", "error", err)
		slog.Warn("Please run docker-compose or start local Postgres to perform full simulation.")
		os.Exit(1)
	}
	defer database.Close()

	// Connect to Redis
	eventBus, err := eventbus.NewEventBus(cfg.RedisAddr, cfg.RedisPassword)
	if err != nil {
		slog.Error("Redis connection failed. Is Redis running?", "error", err)
		os.Exit(1)
	}
	defer eventBus.Close()

	// Clear previous tables for simulation run cleanliness
	cleanSimulationDB(database)

	repo := repository.NewRepository(database)
	riskEngine := risk.NewRiskEngine(eventBus.GetClient())

	// Setup Bank Key
	bankPriv, _ := crypto.GenerateKeyPair()

	// Initialize services
	identitySvc := identity.NewService(repo)
	tokenSvc := token.NewService(repo, bankPriv)
	intentSvc := intent.NewService()
	settleSvc := settlement.NewService(repo, bankPriv, riskEngine)

	// Setup background workers
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outboxWorker := outbox.NewOutboxWorker(repo, eventBus, 200*time.Millisecond)
	go outboxWorker.Start(ctx)

	reconSvc := reconciliation.NewService(repo, tokenSvc, 1*time.Second)
	go reconSvc.Start(ctx)

	// Start Continuous Financial Validator Loop
	financialVal := validator.NewFinancialValidator(repo, 500*time.Millisecond)
	go financialVal.Start(ctx)

	// Seed Account Balances
	aliceAcc := "acc-alice"
	bobAcc := "acc-bob"
	_ = repo.CreateAccount(ctx, aliceAcc, 1000000) // $10,000.00
	_ = repo.CreateAccount(ctx, bobAcc, 20000)      // $200.00

	// Register Device
	devicePriv, _ := crypto.GenerateKeyPair()
	devicePubPEM, _ := crypto.ExportPublicKeyToPEM(&devicePriv.PublicKey)
	aliceDeviceID := "dev-alice-phone"
	_, _ = identitySvc.RegisterDevice(ctx, aliceDeviceID, aliceAcc, devicePubPEM)

	// Register Device Attestation metadata (Secure Enclave / TPM)
	attestation := &domain.DeviceAttestation{
		DeviceID:        aliceDeviceID,
		AttestationType: "TPM_2.0",
		AttestationHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		TrustLevel:      "TRUSTED",
		CreatedAt:       time.Now(),
	}
	err = repo.CreateDeviceAttestation(ctx, attestation)
	if err != nil {
		slog.Error("failed to register device attestation", "error", err)
	} else {
		slog.Info("registered device attestation", "device_id", aliceDeviceID, "trust_level", "TRUSTED")
	}

	slog.Info("simulation state seeded", "alice_balance", 1000000, "bob_balance", 20000)

	// Define transport with 15% packet loss and 20-50ms latency
	transport := &MockTransport{
		lossRate:   0.15,
		latencyMin: 20 * time.Millisecond,
		latencyMax: 50 * time.Millisecond,
	}

	// ----------------------------------------------------
	// SCENARIO 1: Happy Path Offline Settlement
	// ----------------------------------------------------
	slog.Info("--- SCENARIO 1: Happy Path Payment Intent ---")
	
	// Alice requests an offline spending token of $50
	tok, err := tokenSvc.IssueToken(ctx, aliceAcc, 5000, 1*time.Hour)
	if err != nil {
		slog.Error("failed to issue token", "error", err)
		os.Exit(1)
	}
	slog.Info("bank-signed token issued", "token_id", tok.TokenID, "value", tok.Value)

	// Alice goes offline, buys coffee from Bob for $35
	coffeeAmt := int64(3500)
	envelope, _, err := intentSvc.CreateSignedAndEncryptedEnvelope(
		aliceAcc, bobAcc, coffeeAmt, "USD", aliceDeviceID, tok.TokenID, devicePriv, &bankPriv.PublicKey, 10*time.Minute,
	)
	if err != nil {
		slog.Error("failed to create offline payment envelope", "error", err)
		os.Exit(1)
	}
	slog.Info("offline payment intent signed and encrypted", "txn_id", envelope.TxnID)

	// Relayed through transport
	err = transport.Send(ctx, func() error {
		status, err := settleSvc.Settle(ctx, envelope, 1)
		if err != nil {
			return err
		}
		slog.Info("Scenario 1 settled successfully!", "status", status)
		return nil
	})
	if err != nil {
		slog.Warn("Scenario 1 packet failed due to simulated loss, retrying...", "error", err)
		// Retry manually
		status, retryErr := settleSvc.Settle(ctx, envelope, 2)
		if retryErr != nil {
			slog.Error("Scenario 1 failed retry", "error", retryErr)
		} else {
			slog.Info("Scenario 1 settled successfully on retry!", "status", status)
		}
	}

	// ----------------------------------------------------
	// SCENARIO 2: Replay Attack (Nonce Reuse)
	// ----------------------------------------------------
	slog.Info("--- SCENARIO 2: Replay Attack Prevention ---")
	// Try to submit the exact same envelope again
	_, replayErr := settleSvc.Settle(ctx, envelope, 2)
	if replayErr != nil {
		slog.Info("Scenario 2 PASS: Replay attack successfully blocked", "error", replayErr)
	} else {
		slog.Error("Scenario 2 FAIL: Replay attack accepted!")
	}

	// ----------------------------------------------------
	// SCENARIO 3: Double Spending of Offline Token
	// ----------------------------------------------------
	slog.Info("--- SCENARIO 3: Token Double-Spend Protection ---")
	// Issue a new token
	tokDouble, _ := tokenSvc.IssueToken(ctx, aliceAcc, 10000, 1*time.Hour)
	
	// Create two different payments using the SAME token
	env1, _, _ := intentSvc.CreateSignedAndEncryptedEnvelope(aliceAcc, bobAcc, 4000, "USD", aliceDeviceID, tokDouble.TokenID, devicePriv, &bankPriv.PublicKey, 10*time.Minute)
	env2, _, _ := intentSvc.CreateSignedAndEncryptedEnvelope(aliceAcc, bobAcc, 4500, "USD", aliceDeviceID, tokDouble.TokenID, devicePriv, &bankPriv.PublicKey, 10*time.Minute)

	// Settle first payment
	_, err = settleSvc.Settle(ctx, env1, 1)
	if err != nil {
		slog.Error("failed to settle first double-spend component", "error", err)
	} else {
		slog.Info("first payment settled")
	}

	// Settle second payment (must be rejected)
	_, err = settleSvc.Settle(ctx, env2, 1)
	if err != nil {
		slog.Info("Scenario 3 PASS: Double spend blocked", "error", err)
	} else {
		slog.Error("Scenario 3 FAIL: Double spend token succeeded!")
	}

	// ----------------------------------------------------
	// SCENARIO 4: Concurrency Burst & Advisory Locking
	// ----------------------------------------------------
	slog.Info("--- SCENARIO 4: Concurrency Burst & Advisory Locking ---")
	tokConcurrent, _ := tokenSvc.IssueToken(ctx, aliceAcc, 20000, 1*time.Hour)
	envConcurrent, _, _ := intentSvc.CreateSignedAndEncryptedEnvelope(aliceAcc, bobAcc, 15000, "USD", aliceDeviceID, tokConcurrent.TokenID, devicePriv, &bankPriv.PublicKey, 10*time.Minute)

	concurrencyCount := 30
	var wg sync.WaitGroup
	wg.Add(concurrencyCount)

	successes := 0
	failures := 0
	var sMu sync.Mutex

	for i := 0; i < concurrencyCount; i++ {
		go func(threadID int) {
			defer wg.Done()
			_, settleErr := settleSvc.Settle(ctx, envConcurrent, 1)
			sMu.Lock()
			if settleErr == nil {
				successes++
			} else {
				failures++
			}
			sMu.Unlock()
		}(i)
	}

	wg.Wait()
	slog.Info("Scenario 4 Complete", "total_requests", concurrencyCount, "successes", successes, "failures", failures)
	if successes == 1 {
		slog.Info("Scenario 4 PASS: Exactly-once settlement verified under concurrent pressure")
	} else {
		slog.Error("Scenario 4 FAIL: Concurrency violation", "success_count", successes)
	}

	// ----------------------------------------------------
	// SCENARIO 5: Fraud Verification (Invalid Signature)
	// ----------------------------------------------------
	slog.Info("--- SCENARIO 5: Fraud Signature Check ---")
	tokFraud, _ := tokenSvc.IssueToken(ctx, aliceAcc, 10000, 1*time.Hour)
	
	// Create intent using WRONG private key
	attackerPriv, _ := crypto.GenerateKeyPair()
	envFraud, _, _ := intentSvc.CreateSignedAndEncryptedEnvelope(aliceAcc, bobAcc, 5000, "USD", aliceDeviceID, tokFraud.TokenID, attackerPriv, &bankPriv.PublicKey, 10*time.Minute)

	_, err = settleSvc.Settle(ctx, envFraud, 1)
	if err != nil {
		slog.Info("Scenario 5 PASS: Signature verification failure blocked", "error", err)
	} else {
		slog.Error("Scenario 5 FAIL: Invalid signature payment accepted!")
	}

	// ----------------------------------------------------
	// SCENARIO 6: Expired Token Rejection
	// ----------------------------------------------------
	slog.Info("--- SCENARIO 6: Token Expiry Check ---")
	// Issue token with past expiry (simulated clock skew/expiry)
	tokExpired, _ := tokenSvc.IssueToken(ctx, aliceAcc, 5000, -1*time.Minute)
	envExpired, _, _ := intentSvc.CreateSignedAndEncryptedEnvelope(aliceAcc, bobAcc, 4000, "USD", aliceDeviceID, tokExpired.TokenID, devicePriv, &bankPriv.PublicKey, 10*time.Minute)

	_, err = settleSvc.Settle(ctx, envExpired, 1)
	if err != nil {
		slog.Info("Scenario 6 PASS: Expired token rejected", "error", err)
	} else {
		slog.Error("Scenario 6 FAIL: Expired token accepted!")
	}

	// ----------------------------------------------------
	// SCENARIO 7: 1000 Concurrent Goroutines Stress-Test
	// ----------------------------------------------------
	slog.Info("--- SCENARIO 7: 1000 Concurrent Workers Stress-Test ---")
	tokStress, _ := tokenSvc.IssueToken(ctx, aliceAcc, 80000, 1*time.Hour)
	envStress, _, _ := intentSvc.CreateSignedAndEncryptedEnvelope(aliceAcc, bobAcc, 60000, "USD", aliceDeviceID, tokStress.TokenID, devicePriv, &bankPriv.PublicKey, 10*time.Minute)

	stressWorkers := 1000
	var wgStress sync.WaitGroup
	wgStress.Add(stressWorkers)

	stressSuccesses := 0
	stressFailures := 0
	var stressMu sync.Mutex

	for i := 0; i < stressWorkers; i++ {
		go func(workerID int) {
			defer wgStress.Done()
			_, err := settleSvc.Settle(ctx, envStress, 1)
			stressMu.Lock()
			if err == nil {
				stressSuccesses++
			} else {
				stressFailures++
			}
			stressMu.Unlock()
		}(i)
	}

	wgStress.Wait()
	slog.Info("Scenario 7 Stress-Test Complete", "workers", stressWorkers, "successes", stressSuccesses, "failures", stressFailures)
	if stressSuccesses == 1 {
		slog.Info("Scenario 7 PASS: Exactly-once settlement verified under 1000 concurrent workers")
	} else {
		slog.Error("Scenario 7 FAIL: Concurrency violation under stress", "success_count", stressSuccesses)
	}

	// ----------------------------------------------------
	// SCENARIO 8: Byzantine Relay Simulation
	// ----------------------------------------------------
	slog.Info("--- SCENARIO 8: Byzantine Relay Handling ---")
	tokRelay, _ := tokenSvc.IssueToken(ctx, aliceAcc, 50000, 1*time.Hour)
	directClient := &api.DirectSettlementClient{SettleSvc: settleSvc}

	behaviors := []string{"spam", "delayed", "lazy", "byzantine"}
	for _, behavior := range behaviors {
		slog.Info("Testing Byzantine relay behavior", "type", behavior)
		
		envRelay, _, _ := intentSvc.CreateSignedAndEncryptedEnvelope(
			aliceAcc, bobAcc, 2000, "USD", aliceDeviceID, tokRelay.TokenID, devicePriv, &bankPriv.PublicKey, 10*time.Minute,
		)
		
		byzClient := &ByzantineRelayClient{
			client:   directClient,
			behavior: behavior,
		}
		
		relayNode := relay.NewService(repo, byzClient, fmt.Sprintf("byz-relay-%s", behavior), 3)
		
		err := relayNode.ReceiveAndRelay(ctx, envRelay, 1)
		if err != nil {
			slog.Warn("Byzantine relay packet rejected by entry check", "type", behavior, "error", err)
		} else {
			slog.Info("Byzantine relay packet accepted for delivery", "type", behavior)
		}
		
		time.Sleep(300 * time.Millisecond)
	}

	// ----------------------------------------------------
	// SCENARIO 9: Postgres & Redis Chaos Injection
	// ----------------------------------------------------
	slog.Info("--- SCENARIO 9: Chaos Injection (DB / Redis Outages) ---")
	chaosCtrl := chaos.GetController()
	tokChaos, _ := tokenSvc.IssueToken(ctx, aliceAcc, 30000, 1*time.Hour)
	envChaos, _, _ := intentSvc.CreateSignedAndEncryptedEnvelope(
		aliceAcc, bobAcc, 5000, "USD", aliceDeviceID, tokChaos.TokenID, devicePriv, &bankPriv.PublicKey, 10*time.Minute,
	)

	// 1. Inject Postgres outage
	slog.Info("Simulating PostgreSQL outage...")
	chaosCtrl.SetPostgresOffline(true)
	
	_, err = settleSvc.Settle(ctx, envChaos, 1)
	if err != nil {
		slog.Info("Scenario 9 PASS: Transaction safely blocked when DB was offline", "error", err)
	} else {
		slog.Error("Scenario 9 FAIL: Transaction succeeded even though DB was offline!")
	}
	
	chaosCtrl.SetPostgresOffline(false)
	slog.Info("PostgreSQL recovered.")
	
	// 2. Inject Redis outage (transaction outbox holds it)
	slog.Info("Simulating Redis outage...")
	chaosCtrl.SetRedisOffline(true)
	
	statusChaos, err := settleSvc.Settle(ctx, envChaos, 1)
	if err != nil {
		slog.Error("Scenario 9 FAIL: Settlement failed even though DB was online", "error", err)
	} else {
		slog.Info("Scenario 9 PASS: Settlement succeeded with Redis offline (recorded in outbox)", "status", statusChaos)
	}
	
	time.Sleep(500 * time.Millisecond)
	
	chaosCtrl.SetRedisOffline(false)
	slog.Info("Redis recovered.")
	time.Sleep(1 * time.Second)

	// ----------------------------------------------------
	// SCENARIO 10: Raft Consensus Cluster Simulation
	// ----------------------------------------------------
	slog.Info("--- SCENARIO 10: Raft Consensus Cluster Simulation ---")
	clusterSim := cluster.NewCluster()
	
	err = clusterSim.ProposeCommand(ctx, "SETTLE_TXN_alice_bob_1000")
	if err != nil {
		slog.Error("Raft proposal failed", "error", err)
	} else {
		slog.Info("Raft proposal committed successfully on quorum")
	}
	
	slog.Info("Crashing current leader...", "leader", clusterSim.LeaderID)
	clusterSim.CrashNode(clusterSim.LeaderID)
	
	err = clusterSim.ProposeCommand(ctx, "SETTLE_TXN_alice_bob_2000")
	if err != nil {
		slog.Info("Raft proposal failed as expected during leader outage", "error", err)
	} else {
		slog.Error("Raft proposal succeeded during leader outage!")
	}
	
	time.Sleep(200 * time.Millisecond) // wait for leader election
	
	err = clusterSim.ProposeCommand(ctx, "SETTLE_TXN_alice_bob_3000")
	if err != nil {
		slog.Error("Raft proposal failed after election", "error", err)
	} else {
		slog.Info("Raft proposal committed on new leader quorum!")
	}

	// Trigger manual reconciliation run
	slog.Info("Triggering reconciliation sweep...")
	releasedExpired, _ := tokenSvc.ReleaseExpiredTokens(ctx)
	slog.Info("Reconciliation sweep complete", "released_expired_token_funds", releasedExpired)

	// Verify final balances
	aliceBal, _ := repo.GetBalance(ctx, aliceAcc)
	bobBal, _ := repo.GetBalance(ctx, bobAcc)
	slog.Info("Simulation Final Authoritative Balances",
		"alice_avail", aliceBal.AvailableBalance,
		"alice_reserved", aliceBal.ReservedBalance,
		"bob_avail", bobBal.AvailableBalance,
	)

	// Check for ledger inconsistencies
	inconsistencies, _ := repo.CheckLedgerInconsistencies(ctx)
	if len(inconsistencies) == 0 {
		slog.Info("Ledger is completely consistent and balanced!")
	} else {
		slog.Error("CRITICAL: Ledger imbalances found", "count", len(inconsistencies))
	}

	// Print metrics summary
	slog.Info("Simulation transport stats", "packet_delivered", transport.packetDelivered, "packet_dropped", transport.packetDropped)
}

func cleanSimulationDB(database *db.DB) {
	tables := []string{
		"ledger_entries",
		"nonce_registry",
		"payment_intents",
		"offline_tokens",
		"devices",
		"account_balances",
		"relay_attempts",
		"audit_events",
		"outbox_events",
		"dead_letter_events",
		"device_attestations",
	}
	for _, table := range tables {
		_, _ = database.Exec(fmt.Sprintf("TRUNCATE TABLE %s CASCADE", table))
	}
}
