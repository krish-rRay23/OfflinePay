package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"offlinepay/internal/config"
	"offlinepay/internal/crypto"
	"offlinepay/internal/db"
	"offlinepay/internal/domain"
	"offlinepay/internal/eventbus"
	"offlinepay/internal/intent"
	"offlinepay/internal/repository"
	"offlinepay/internal/risk"
	"offlinepay/internal/settlement"
	"offlinepay/internal/token"

	"github.com/redis/go-redis/v9"
)

func main() {
	// Setup logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelWarn}))
	slog.SetDefault(logger)

	fmt.Println("Starting Storm Replay Stress Test (10,000 duplicate intents)...")

	// Load config
	cfg := config.LoadConfig()

	// Connect to Database
	database, err := db.Connect(cfg.DBURL)
	if err != nil {
		fmt.Printf("Failed to connect to PostgreSQL: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	repo := repository.NewRepository(database)
	ctx := context.Background()

	// Connect to Event Bus (Redis)
	var rdb *redis.Client
	eventBus, err := eventbus.NewEventBus(cfg.RedisAddr, cfg.RedisPassword)
	if err != nil {
		fmt.Printf("Warning: Failed to connect to Redis: %v. Running without Redis.\n", err)
	} else {
		defer eventBus.Close()
		rdb = eventBus.GetClient()
	}

	// Setup clean state
	senderID := "sender-storm"
	receiverID := "receiver-storm"
	deviceID := "device-storm"

	// Clean up existing data in correct dependency order to avoid foreign key violations
	queries := []struct {
		desc  string
		query string
		args  []interface{}
	}{
		{
			desc:  "Delete from ledger_entries",
			query: `DELETE FROM ledger_entries WHERE account_id IN ($1, $2) OR txn_id IN (SELECT txn_id FROM payment_intents WHERE sender_id = $1 OR receiver_id = $2)`,
			args:  []interface{}{senderID, receiverID},
		},
		{
			desc:  "Delete from nonce_registry",
			query: `DELETE FROM nonce_registry WHERE txn_id IN (SELECT txn_id FROM payment_intents WHERE sender_id = $1 OR receiver_id = $2)`,
			args:  []interface{}{senderID, receiverID},
		},
		{
			desc:  "Delete from payment_events",
			query: `DELETE FROM payment_events WHERE txn_id IN (SELECT txn_id FROM payment_intents WHERE sender_id = $1 OR receiver_id = $2)`,
			args:  []interface{}{senderID, receiverID},
		},
		{
			desc:  "Delete from payment_intents",
			query: `DELETE FROM payment_intents WHERE sender_id = $1 OR receiver_id = $2`,
			args:  []interface{}{senderID, receiverID},
		},
		{
			desc:  "Delete from offline_tokens",
			query: `DELETE FROM offline_tokens WHERE owner_id = $1`,
			args:  []interface{}{senderID},
		},
		{
			desc:  "Delete from devices",
			query: `DELETE FROM devices WHERE owner_id = $1`,
			args:  []interface{}{senderID},
		},
		{
			desc:  "Delete from account_balances",
			query: `DELETE FROM account_balances WHERE account_id IN ($1, $2)`,
			args:  []interface{}{senderID, receiverID},
		},
	}

	for _, q := range queries {
		if _, err := database.ExecContext(ctx, q.query, q.args...); err != nil {
			fmt.Printf("Warning during cleanup (%s): %v\n", q.desc, err)
		}
	}

	// Create accounts
	err = repo.CreateAccount(ctx, senderID, 10000)
	if err != nil {
		fmt.Printf("Failed to create sender account: %v\n", err)
		os.Exit(1)
	}
	err = repo.CreateAccount(ctx, receiverID, 10000)
	if err != nil {
		fmt.Printf("Failed to create receiver account: %v\n", err)
		os.Exit(1)
	}

	// Generate keys
	bankKey, err := crypto.GenerateKeyPair()
	if err != nil {
		fmt.Printf("Failed to generate bank keys: %v\n", err)
		os.Exit(1)
	}

	deviceKey, err := crypto.GenerateKeyPair()
	if err != nil {
		fmt.Printf("Failed to generate device keys: %v\n", err)
		os.Exit(1)
	}

	devicePubPEM, _ := crypto.ExportPublicKeyToPEM(&deviceKey.PublicKey)

	// Create device
	err = repo.CreateDevice(ctx, &domain.Device{
		DeviceID:   deviceID,
		OwnerID:    senderID,
		PublicKey:  devicePubPEM,
		TrustScore: 1.0,
		Status:     domain.DeviceActive,
		CreatedAt:  time.Now(),
	})
	if err != nil {
		fmt.Printf("Failed to create device: %v\n", err)
		os.Exit(1)
	}

	// Issue token
	tokenSvc := token.NewService(repo, bankKey)
	tok, err := tokenSvc.IssueToken(ctx, senderID, 1000, 24*time.Hour)
	if err != nil {
		fmt.Printf("Failed to issue token: %v\n", err)
		os.Exit(1)
	}

	// Create signed & encrypted envelope (payment intent)
	intentSvc := intent.NewService()
	envelope, _, err := intentSvc.CreateSignedAndEncryptedEnvelope(
		senderID,
		receiverID,
		100, // payment of 100 cents using 1000 cents token
		"USD",
		deviceID,
		tok.TokenID,
		deviceKey,
		&bankKey.PublicKey,
		1*time.Hour,
	)
	if err != nil {
		fmt.Printf("Failed to create envelope: %v\n", err)
		os.Exit(1)
	}

	// Instantiate Settlement service
	riskEngine := risk.NewRiskEngine(rdb)
	settleSvc := settlement.NewService(repo, bankKey, riskEngine)

	var successCount int64
	var duplicateCount int64
	var otherFailCount int64

	concurrency := 100
	totalRequests := 10000

	sem := make(chan struct{}, concurrency)

	fmt.Printf("Dispatching %d duplicate transactions...\n", totalRequests)
	startTime := time.Now()

	var wg sync.WaitGroup
	wg.Add(totalRequests)

	for i := 0; i < totalRequests; i++ {
		sem <- struct{}{}
		go func() {
			defer func() {
				<-sem
				wg.Done()
			}()
			// Settle duplicate transaction
			status, err := settleSvc.Settle(context.Background(), envelope, 1)
			if err == nil && status == domain.StateSettled {
				atomic.AddInt64(&successCount, 1)
			} else {
				if status == domain.StateDuplicate || (err != nil && (err.Error() == "spending token already consumed" || err.Error() == "token_already_consumed_concurrent")) {
					atomic.AddInt64(&duplicateCount, 1)
				} else {
					atomic.AddInt64(&otherFailCount, 1)
				}
			}
		}()
	}

	wg.Wait()

	duration := time.Since(startTime)
	fmt.Printf("Storm execution finished in %v.\n", duration)
	fmt.Printf("--- Storm Replay Rejection Metrics ---\n")
	fmt.Printf("Total Requests: %d\n", totalRequests)
	fmt.Printf("Successful Settlements (Expect 1): %d\n", successCount)
	fmt.Printf("Duplicate Rejections (Expect 9999): %d\n", duplicateCount)
	fmt.Printf("Other Failures: %d\n", otherFailCount)

	if successCount != 1 {
		fmt.Printf("FAIL: Expected exactly 1 successful settlement, got %d\n", successCount)
		os.Exit(1)
	}
	if duplicateCount+otherFailCount != 9999 {
		fmt.Printf("FAIL: Expected exactly 9999 duplicate/failed rejections, got %d\n", duplicateCount+otherFailCount)
		os.Exit(1)
	}

	fmt.Println("SUCCESS: Replay protection holds perfectly under storm load!")
}
