package settlement

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"offlinepay/internal/crypto"
	"offlinepay/internal/db"
	"offlinepay/internal/domain"
	"offlinepay/internal/intent"
	"offlinepay/internal/repository"
	"offlinepay/internal/risk"
)

func BenchmarkSettlement(b *testing.B) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:root@localhost:5432/offlinepay_test?sslmode=disable"
	}

	database, err := db.Connect(dbURL)
	if err != nil {
		b.Skipf("skipping benchmark; database connection failed: %v", err)
		return
	}
	defer database.Close()

	repo := repository.NewRepository(database)
	ctx := context.Background()

	// Create test device
	deviceKey, _ := crypto.GenerateKeyPair()
	devicePubPEM, _ := crypto.ExportPublicKeyToPEM(&deviceKey.PublicKey)
	deviceID := "bench-device"
	
	// Clean previous device if exists
	_, _ = database.ExecContext(ctx, "DELETE FROM devices WHERE device_id = $1", deviceID)
	
	err = repo.CreateDevice(ctx, &domain.Device{
		DeviceID:   deviceID,
		OwnerID:    "bench-sender",
		PublicKey:  devicePubPEM,
		TrustScore: 1.0,
		Status:     domain.DeviceActive,
		CreatedAt:  time.Now(),
	})
	if err != nil {
		b.Fatalf("failed to create device: %v", err)
	}

	bankPriv, _ := crypto.GenerateKeyPair()
	riskEngine := risk.NewRiskEngine(nil)
	settleSvc := NewService(repo, bankPriv, riskEngine)
	intentSvc := intent.NewService()

	// Create token
	tokenID := "bench-token"
	_, _ = database.ExecContext(ctx, "DELETE FROM offline_tokens WHERE token_id = $1", tokenID)
	_, _ = database.ExecContext(ctx, `
		INSERT INTO offline_tokens (token_id, owner_id, value, expiry, consumed, token_signature)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, tokenID, "bench-sender", 100000, time.Now().Add(24*time.Hour), false, "dummy-sig")

	// Pre-setup account balances
	_, _ = database.ExecContext(ctx, "DELETE FROM account_balances WHERE account_id IN ('bench-sender', 'bench-receiver')")
	_ = repo.CreateAccount(ctx, "bench-sender", 10000000)
	_ = repo.CreateAccount(ctx, "bench-receiver", 10000000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		env, _, err := intentSvc.CreateSignedAndEncryptedEnvelope(
			"bench-sender",
			"bench-receiver",
			10,
			"USD",
			deviceID,
			tokenID,
			deviceKey,
			&bankPriv.PublicKey,
			1*time.Hour,
		)
		if err != nil {
			b.Fatalf("failed to create envelope: %v", err)
		}

		_, _ = settleSvc.Settle(ctx, env, 1)
	}
}

func BenchmarkReplayProtection(b *testing.B) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:root@localhost:5432/offlinepay_test?sslmode=disable"
	}

	database, err := db.Connect(dbURL)
	if err != nil {
		b.Skipf("skipping benchmark; database connection failed: %v", err)
		return
	}
	defer database.Close()

	repo := repository.NewRepository(database)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nonce := fmt.Sprintf("bench-nonce-replay-%d-%d", b.N, i)
		txnID := fmt.Sprintf("bench-txn-replay-%d-%d", b.N, i)
		_ = repo.RegisterNonce(ctx, nil, nonce, txnID, time.Now().Add(1*time.Hour))
	}
}

func BenchmarkLedgerCommit(b *testing.B) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:root@localhost:5432/offlinepay_test?sslmode=disable"
	}

	database, err := db.Connect(dbURL)
	if err != nil {
		b.Skipf("skipping benchmark; database connection failed: %v", err)
		return
	}
	defer database.Close()

	repo := repository.NewRepository(database)
	ctx := context.Background()

	_, _ = database.ExecContext(ctx, "DELETE FROM account_balances WHERE account_id IN ('ledger-sender', 'ledger-receiver')")
	_ = repo.CreateAccount(ctx, "ledger-sender", 1000000)
	_ = repo.CreateAccount(ctx, "ledger-receiver", 1000000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = repo.WithTx(ctx, func(tx *sql.Tx) error {
			_, _ = tx.ExecContext(ctx, "UPDATE account_balances SET available_balance = available_balance - 1 WHERE account_id = $1", "ledger-sender")
			_, _ = tx.ExecContext(ctx, "UPDATE account_balances SET available_balance = available_balance + 1 WHERE account_id = $1", "ledger-receiver")
			return nil
		})
	}
}

func BenchmarkProjection(b *testing.B) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:root@localhost:5432/offlinepay_test?sslmode=disable"
	}

	database, err := db.Connect(dbURL)
	if err != nil {
		b.Skipf("skipping benchmark; database connection failed: %v", err)
		return
	}
	defer database.Close()

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		txnID := fmt.Sprintf("proj-txn-%d-%d", b.N, i)
		_, _ = database.ExecContext(ctx, `
			INSERT INTO payment_read_projections (txn_id, sender_id, receiver_id, amount, status, relay_hops, settled_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, txnID, "sender", "receiver", 100, "SETTLED", 1, time.Now())
	}
}
