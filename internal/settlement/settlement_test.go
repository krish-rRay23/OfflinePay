package settlement

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"offlinepay/internal/crypto"
	"offlinepay/internal/db"
	"offlinepay/internal/domain"
	"offlinepay/internal/identity"
	"offlinepay/internal/intent"
	"offlinepay/internal/repository"
	"offlinepay/internal/risk"
	"offlinepay/internal/token"
)

func setupTestDB(t *testing.T) (*db.DB, func()) {
	// Try standard local test database url
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:root@localhost:5432/offlinepay_test?sslmode=disable"
	}
	
	// Open connection to postgres to check if it's running.
	// If it fails, try the default docker compose db URL or check if we can run it.
	// In local testing environments without postgres, we skip the test rather than failing.
	conn, err := sql.Open("postgres", dbURL)
	if err != nil {
		t.Logf("sql.Open err: %v", err)
		t.Skip("PostgreSQL test database not available. Skipping integration test.")
		return nil, nil
	}
	defer conn.Close()

	err = conn.Ping()
	if err != nil {
		t.Logf("conn.Ping test DB err: %v", err)
		// Try fallback: default development DB (offlinepay) instead of offlinepay_test
		dbURL = "postgres://postgres:root@localhost:5432/offlinepay?sslmode=disable"
		connFallback, errFallback := sql.Open("postgres", dbURL)
		if errFallback != nil {
			t.Logf("sql.Open fallback err: %v", errFallback)
			t.Skip("PostgreSQL test database not reachable. Skipping integration test.")
			return nil, nil
		}
		defer connFallback.Close()
		if errFallback = connFallback.Ping(); errFallback != nil {
			t.Logf("conn.Ping fallback DB err: %v", errFallback)
			t.Skip("PostgreSQL test database not reachable. Skipping integration test.")
			return nil, nil
		}
	}

	// Connect and apply migrations
	database, err := db.Connect(dbURL)
	if err != nil {
		t.Fatalf("failed to connect and migrate test db: %v", err)
	}

	// Clean up database tables before each test
	cleanDB(t, database)

	cleanup := func() {
		cleanDB(t, database)
		database.Close()
	}

	return database, cleanup
}

func cleanDB(t *testing.T, database *db.DB) {
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
	}
	for _, table := range tables {
		_, err := database.Exec(fmt.Sprintf("TRUNCATE TABLE %s CASCADE", table))
		if err != nil {
			t.Fatalf("failed to truncate table %s: %v", table, err)
		}
	}

	// Seed UNKNOWN rows for foreign key constraints in failed/rejected intents
	_, err := database.Exec(`
		INSERT INTO devices (device_id, owner_id, public_key, trust_score, status)
		VALUES ('UNKNOWN', 'SYSTEM', 'dummy', 0.0, 'ACTIVE')
		ON CONFLICT DO NOTHING
	`)
	if err != nil {
		t.Fatalf("failed to seed UNKNOWN device: %v", err)
	}

	_, err = database.Exec(`
		INSERT INTO offline_tokens (token_id, owner_id, value, expiry, consumed, token_signature)
		VALUES ('UNKNOWN', 'SYSTEM', 0, '2099-12-31 23:59:59+00', TRUE, 'dummy')
		ON CONFLICT DO NOTHING
	`)
	if err != nil {
		t.Fatalf("failed to seed UNKNOWN token: %v", err)
	}
}

func TestSettle_Integration(t *testing.T) {
	database, cleanup := setupTestDB(t)
	if database == nil {
		return
	}
	defer cleanup()

	ctx := context.Background()
	repo := repository.NewRepository(database)

	// Generate keypairs
	bankPriv, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("crypto error: %v", err)
	}

	devicePriv, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("crypto error: %v", err)
	}

	// Instantiate services
	identitySvc := identity.NewService(repo)
	tokenSvc := token.NewService(repo, bankPriv)
	settleSvc := NewService(repo, bankPriv, risk.NewRiskEngine(nil))
	intentSvc := intent.NewService()

	// Seed account balances
	senderID := "user-alice"
	receiverID := "user-bob"
	deviceID := "dev-alice-phone"

	err = repo.CreateAccount(ctx, senderID, 10000) // $100.00
	if err != nil {
		t.Fatalf("failed to seed balance: %v", err)
	}
	err = repo.CreateAccount(ctx, receiverID, 500) // $5.00
	if err != nil {
		t.Fatalf("failed to seed balance: %v", err)
	}

	// Register device
	devicePubPEM, _ := crypto.ExportPublicKeyToPEM(&devicePriv.PublicKey)
	_, err = identitySvc.RegisterDevice(ctx, deviceID, senderID, devicePubPEM)
	if err != nil {
		t.Fatalf("failed to register device: %v", err)
	}

	// 1. Happy Path: Issue token, offline spend, settle
	tokenVal := int64(3000) // $30.00
	tok, err := tokenSvc.IssueToken(ctx, senderID, tokenVal, 1*time.Hour)
	if err != nil {
		t.Fatalf("failed to issue token: %v", err)
	}

	// Verify balance reservation
	senderBal, err := repo.GetBalance(ctx, senderID)
	if err != nil {
		t.Fatalf("failed to get balance: %v", err)
	}
	if senderBal.AvailableBalance != 7000 || senderBal.ReservedBalance != 3000 {
		t.Errorf("wrong reservation: expected avail=7000, res=3000; got avail=%d, res=%d", senderBal.AvailableBalance, senderBal.ReservedBalance)
	}

	// Create signed & encrypted offline envelope
	payAmt := int64(2500) // $25.00
	envelope, _, err := intentSvc.CreateSignedAndEncryptedEnvelope(
		senderID, receiverID, payAmt, "USD", deviceID, tok.TokenID, devicePriv, &bankPriv.PublicKey, 5*time.Minute,
	)
	if err != nil {
		t.Fatalf("failed to create envelope: %v", err)
	}

	// Settle transaction
	status, err := settleSvc.Settle(ctx, envelope, 1)
	if err != nil {
		t.Fatalf("settlement failed: %v", err)
	}
	if status != domain.StateSettled {
		t.Fatalf("expected settled state, got %s", status)
	}

	// Verify balances post-settlement
	// Alice spent $25.00 of the $30.00 token.
	// Net change:
	// Alice available: $70.00 (before) + $5.00 (change return) = $75.00.
	// Alice reserved: $30.00 (before) - $30.00 (consumed) = $0.
	// Bob available: $5.00 (before) + $25.00 (payment) = $30.00.
	senderBal, _ = repo.GetBalance(ctx, senderID)
	receiverBal, _ := repo.GetBalance(ctx, receiverID)

	if senderBal.AvailableBalance != 7500 || senderBal.ReservedBalance != 0 {
		t.Errorf("wrong sender balances: expected avail=7500, res=0; got avail=%d, res=%d", senderBal.AvailableBalance, senderBal.ReservedBalance)
	}
	if receiverBal.AvailableBalance != 3000 {
		t.Errorf("wrong receiver balance: expected avail=3000, got %d", receiverBal.AvailableBalance)
	}

	// Verify ledger entries
	imbalances, err := repo.CheckLedgerInconsistencies(ctx)
	if err != nil {
		t.Fatalf("failed to check ledger consistency: %v", err)
	}
	if len(imbalances) > 0 {
		t.Errorf("unbalanced ledger entries found: %v", imbalances)
	}

	// 2. Replay Protection: Re-settle same envelope must yield duplicate status
	status2, err2 := settleSvc.Settle(ctx, envelope, 2)
	if err2 == nil {
		t.Fatal("expected error on replaying identical envelope")
	}
	if status2 != domain.StateDuplicate {
		t.Errorf("expected state DUPLICATE, got %s", status2)
	}
}

func TestSettle_DoubleSpendToken(t *testing.T) {
	database, cleanup := setupTestDB(t)
	if database == nil {
		return
	}
	defer cleanup()

	ctx := context.Background()
	repo := repository.NewRepository(database)
	bankPriv, _ := crypto.GenerateKeyPair()
	devicePriv, _ := crypto.GenerateKeyPair()
	tokenSvc := token.NewService(repo, bankPriv)
	settleSvc := NewService(repo, bankPriv, risk.NewRiskEngine(nil))
	intentSvc := intent.NewService()

	// Seed accounts
	senderID := "alice"
	receiverID := "bob"
	deviceID := "device"
	_ = repo.CreateAccount(ctx, senderID, 10000)
	_ = repo.CreateAccount(ctx, receiverID, 0)
	devicePubPEM, _ := crypto.ExportPublicKeyToPEM(&devicePriv.PublicKey)
	_ = repo.CreateDevice(ctx, &domain.Device{DeviceID: deviceID, OwnerID: senderID, PublicKey: devicePubPEM, Status: domain.DeviceActive, TrustScore: 1.0, CreatedAt: time.Now()})

	// Issue token
	tok, _ := tokenSvc.IssueToken(ctx, senderID, 5000, 1*time.Hour)

	// Create Envelope A
	envA, _, _ := intentSvc.CreateSignedAndEncryptedEnvelope(senderID, receiverID, 2000, "USD", deviceID, tok.TokenID, devicePriv, &bankPriv.PublicKey, 10*time.Minute)
	// Create Envelope B using the SAME token
	envB, _, _ := intentSvc.CreateSignedAndEncryptedEnvelope(senderID, receiverID, 3000, "USD", deviceID, tok.TokenID, devicePriv, &bankPriv.PublicKey, 10*time.Minute)

	// Settle A (succeeds)
	statusA, err := settleSvc.Settle(ctx, envA, 1)
	if err != nil || statusA != domain.StateSettled {
		t.Fatalf("first payment failed: %v", err)
	}

	// Settle B (fails double spend)
	statusB, err := settleSvc.Settle(ctx, envB, 1)
	if err == nil {
		t.Fatal("expected second payment using same token to fail double spend check")
	}
	if statusB != domain.StateDuplicate {
		t.Errorf("expected status DUPLICATE, got %s", statusB)
	}
}

func TestSettle_InvalidSignature(t *testing.T) {
	database, cleanup := setupTestDB(t)
	if database == nil {
		return
	}
	defer cleanup()

	ctx := context.Background()
	repo := repository.NewRepository(database)
	bankPriv, _ := crypto.GenerateKeyPair()
	devicePriv, _ := crypto.GenerateKeyPair()
	tokenSvc := token.NewService(repo, bankPriv)
	settleSvc := NewService(repo, bankPriv, risk.NewRiskEngine(nil))

	// Seed accounts
	senderID := "alice"
	receiverID := "bob"
	deviceID := "device"
	_ = repo.CreateAccount(ctx, senderID, 10000)
	_ = repo.CreateAccount(ctx, receiverID, 0)
	
	// Create a device with WRONG public key
	otherKey, _ := crypto.GenerateKeyPair()
	wrongPubPEM, _ := crypto.ExportPublicKeyToPEM(&otherKey.PublicKey)
	_ = repo.CreateDevice(ctx, &domain.Device{DeviceID: deviceID, OwnerID: senderID, PublicKey: wrongPubPEM, Status: domain.DeviceActive, TrustScore: 1.0, CreatedAt: time.Now()})

	tok, _ := tokenSvc.IssueToken(ctx, senderID, 5000, 1*time.Hour)

	intentSvc := intent.NewService()
	env, _, _ := intentSvc.CreateSignedAndEncryptedEnvelope(senderID, receiverID, 2000, "USD", deviceID, tok.TokenID, devicePriv, &bankPriv.PublicKey, 10*time.Minute)

	// Settle (fails due to signature verify failure)
	status, err := settleSvc.Settle(ctx, env, 1)
	if err == nil {
		t.Fatal("expected settlement to fail due to signature mismatch")
	}
	if status != domain.StateRejected {
		t.Errorf("expected status REJECTED, got %s", status)
	}
}

func TestSettle_Concurrency(t *testing.T) {
	database, cleanup := setupTestDB(t)
	if database == nil {
		return
	}
	defer cleanup()

	ctx := context.Background()
	repo := repository.NewRepository(database)
	bankPriv, _ := crypto.GenerateKeyPair()
	devicePriv, _ := crypto.GenerateKeyPair()
	tokenSvc := token.NewService(repo, bankPriv)
	settleSvc := NewService(repo, bankPriv, risk.NewRiskEngine(nil))
	intentSvc := intent.NewService()

	// Seed accounts
	senderID := "alice"
	receiverID := "bob"
	deviceID := "device"
	_ = repo.CreateAccount(ctx, senderID, 10000)
	_ = repo.CreateAccount(ctx, receiverID, 0)
	devicePubPEM, _ := crypto.ExportPublicKeyToPEM(&devicePriv.PublicKey)
	_ = repo.CreateDevice(ctx, &domain.Device{DeviceID: deviceID, OwnerID: senderID, PublicKey: devicePubPEM, Status: domain.DeviceActive, TrustScore: 1.0, CreatedAt: time.Now()})

	tok, _ := tokenSvc.IssueToken(ctx, senderID, 5000, 1*time.Hour)
	env, _, _ := intentSvc.CreateSignedAndEncryptedEnvelope(senderID, receiverID, 3000, "USD", deviceID, tok.TokenID, devicePriv, &bankPriv.PublicKey, 10*time.Minute)

	// Concurrent runs
	concurrency := 10
	var wg sync.WaitGroup
	wg.Add(concurrency)

	errorsChan := make(chan error, concurrency)
	statusChan := make(chan string, concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			status, err := settleSvc.Settle(ctx, env, 1)
			if err != nil {
				errorsChan <- err
			}
			statusChan <- status
		}()
	}

	wg.Wait()
	close(errorsChan)
	close(statusChan)

	settledCount := 0
	duplicateCount := 0

	for status := range statusChan {
		if status == domain.StateSettled {
			settledCount++
		} else if status == domain.StateDuplicate {
			duplicateCount++
		}
	}

	// Verify that exactly ONE settlement succeeded, and the rest were blocked/rejected as duplicate
	if settledCount != 1 {
		t.Errorf("concurrency violation: expected exactly 1 successful settlement, got %d", settledCount)
	}

	// Check final balances
	senderBal, _ := repo.GetBalance(ctx, senderID)
	receiverBal, _ := repo.GetBalance(ctx, receiverID)

	// Alice had 10000. Reserved 5000 (avail=5000). Paid 3000. Alice change returned = 2000.
	// Alice final balance: avail = 5000 + 2000 = 7000. res = 0.
	// Bob final balance: 3000.
	if senderBal.AvailableBalance != 7000 {
		t.Errorf("wrong sender available balance: expected 7000, got %d", senderBal.AvailableBalance)
	}
	if receiverBal.AvailableBalance != 3000 {
		t.Errorf("wrong receiver available balance: expected 3000, got %d", receiverBal.AvailableBalance)
	}
}
