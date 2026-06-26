package repository

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"offlinepay/internal/db"
	"offlinepay/internal/domain"
)

func TestRepositoryOperations(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:root@localhost:5432/offlinepay_test?sslmode=disable"
	}

	// 1. Establish connection to isolated test DB
	database, err := db.Connect(dbURL)
	if err != nil {
		t.Skipf("skipping repository integration tests; database connection failed: %v", err)
		return
	}
	defer database.Close()

	repo := NewRepository(database)
	ctx := context.Background()

	// Clean tables before test run
	clearTestTables(database)
	defer clearTestTables(database)

	t.Run("Create and Get Device Identity", func(t *testing.T) {
		dev := &domain.Device{
			DeviceID:   "test-dev-123",
			OwnerID:    "owner-alice",
			PublicKey:  "pem-key-data",
			TrustScore: 0.95,
			Status:     domain.DeviceActive,
			CreatedAt:  time.Now(),
		}

		err := repo.CreateDevice(ctx, dev)
		if err != nil {
			t.Fatalf("failed to create device: %v", err)
		}

		fetched, err := repo.GetDevice(ctx, "test-dev-123")
		if err != nil {
			t.Fatalf("failed to fetch device: %v", err)
		}

		if fetched.OwnerID != "owner-alice" || fetched.TrustScore != 0.95 {
			t.Errorf("fetched device data mismatch: %+v", fetched)
		}
	})

	t.Run("Account Balances Available and Reserved updates", func(t *testing.T) {
		err := repo.CreateAccount(ctx, "acc-bob", 10000) // $100
		if err != nil {
			t.Fatalf("failed to create account: %v", err)
		}

		bal, err := repo.GetBalance(ctx, "acc-bob")
		if err != nil {
			t.Fatalf("failed to retrieve balance: %v", err)
		}
		if bal.AvailableBalance != 10000 || bal.ReservedBalance != 0 {
			t.Errorf("initial balance mismatch: %+v", bal)
		}

		// Perform reservation update inside transaction
		err = repo.WithTx(ctx, func(tx *sql.Tx) error {
			lockedBal, err := repo.GetBalanceForUpdate(ctx, tx, "acc-bob")
			if err != nil {
				return err
			}

			// Move $30 to reserved
			err = repo.UpdateBalance(ctx, tx, "acc-bob", lockedBal.AvailableBalance-3000, lockedBal.ReservedBalance+3000)
			return err
		})
		if err != nil {
			t.Fatalf("transaction failed: %v", err)
		}

		updated, err := repo.GetBalance(ctx, "acc-bob")
		if err != nil {
			t.Fatalf("failed to fetch balance after update: %v", err)
		}
		if updated.AvailableBalance != 7000 || updated.ReservedBalance != 3000 {
			t.Errorf("balance reservation mismatch: %+v", updated)
		}
	})

	t.Run("Dead Letter Queue paginations", func(t *testing.T) {
		// Clear DLQ table first
		_, _ = database.ExecContext(ctx, "DELETE FROM dead_letter_events")

		ev1 := &domain.DeadLetterEvent{Payload: `{"data":"payload-1"}`, FailureReason: "error-1", RetryCount: 1, Timestamp: time.Now()}
		ev2 := &domain.DeadLetterEvent{Payload: `{"data":"payload-2"}`, FailureReason: "error-2", RetryCount: 2, Timestamp: time.Now()}

		if err := repo.CreateDeadLetterEvent(ctx, nil, ev1); err != nil {
			t.Fatalf("failed to create dead letter event 1: %v", err)
		}
		if err := repo.CreateDeadLetterEvent(ctx, nil, ev2); err != nil {
			t.Fatalf("failed to create dead letter event 2: %v", err)
		}

		events, total, err := repo.GetDeadLetterEventsPaginated(ctx, 1, 0)
		if err != nil {
			t.Fatalf("failed to retrieve paginated events: %v", err)
		}

		if total != 2 {
			t.Errorf("expected total count 2, got %d", total)
		}
		if len(events) != 1 {
			t.Errorf("expected paginated list length 1, got %d", len(events))
		}
		if events[0].Payload != `{"data": "payload-1"}` {
			t.Errorf(`expected {"data": "payload-1"}, got %s`, events[0].Payload)
		}
	})
}

func clearTestTables(database *db.DB) {
	ctx := context.Background()
	_, _ = database.ExecContext(ctx, "DELETE FROM dead_letter_events")
	_, _ = database.ExecContext(ctx, "DELETE FROM devices")
	_, _ = database.ExecContext(ctx, "DELETE FROM account_balances")
}
