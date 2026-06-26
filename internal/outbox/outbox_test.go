package outbox

import (
	"context"
	"os"
	"testing"
	"time"

	"offlinepay/internal/chaos"
	"offlinepay/internal/db"
	"offlinepay/internal/eventbus"
	"offlinepay/internal/repository"
)

func TestOutboxProcessingAndDLQRouting(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:root@localhost:5432/offlinepay_test?sslmode=disable"
	}

	database, err := db.Connect(dbURL)
	if err != nil {
		t.Skipf("skipping outbox integration tests; database connection failed: %v", err)
		return
	}
	defer database.Close()

	repo := repository.NewRepository(database)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Clear outbox and DLQ tables
	_, _ = database.ExecContext(ctx, "DELETE FROM outbox_events")
	_, _ = database.ExecContext(ctx, "DELETE FROM dead_letter_events")

	// Start mock Redis server on test port 16379
	err = db.StartMockRedisServer("16379")
	if err != nil {
		t.Fatalf("failed to start mock Redis server: %v", err)
	}

	eventBus, err := eventbus.NewEventBus("localhost:16379", "")
	if err != nil {
		t.Fatalf("failed to connect to mock Redis: %v", err)
	}
	defer eventBus.Close()

	outboxWorker := NewOutboxWorker(repo, eventBus, 50*time.Millisecond)
	outboxWorker.workerCount = 1
	outboxWorker.maxRetries = 2

	t.Run("Pending event is successfully published and marked PUBLISHED", func(t *testing.T) {
		_, err = database.ExecContext(ctx, `
			INSERT INTO outbox_events (stream_name, event_type, payload, status, retry_count)
			VALUES ($1, $2, $3, $4, $5)
		`, "test-success-stream", "TestSuccessEvent", `{"txn_id":"txn-success","stream_name":"test-success-stream","event_type":"TestSuccessEvent"}`, "PENDING", 0)
		if err != nil {
			t.Fatalf("failed to insert test event: %v", err)
		}

		// Run outbox worker briefly in a goroutine
		workerCtx, workerCancel := context.WithCancel(ctx)
		go outboxWorker.Start(workerCtx)

		// Wait for processing
		time.Sleep(150 * time.Millisecond)
		workerCancel()

		// Verify event was processed and deleted/published
		var status string
		err = database.QueryRowContext(ctx, "SELECT status FROM outbox_events WHERE stream_name = $1", "test-success-stream").Scan(&status)
		if err != nil {
			t.Fatalf("failed to query outbox: %v", err)
		}
		if status != "PUBLISHED" {
			t.Errorf("expected PUBLISHED status, got %s", status)
		}
	})

	t.Run("Failing events exceed max retries and are routed to DLQ", func(t *testing.T) {
		// Clean tables
		_, _ = database.ExecContext(ctx, "DELETE FROM outbox_events")
		_, _ = database.ExecContext(ctx, "DELETE FROM dead_letter_events")

		// Insert event that will fail
		_, err = database.ExecContext(ctx, `
			INSERT INTO outbox_events (stream_name, event_type, payload, status, retry_count)
			VALUES ($1, $2, $3, $4, $5)
		`, "test-fail-stream", "TestFailEvent", `{"txn_id":"txn-fail","stream_name":"test-fail-stream","event_type":"TestFailEvent"}`, "PENDING", 0)
		if err != nil {
			t.Fatalf("failed to insert failing test event: %v", err)
		}

		// Inject Redis failure
		chaos.GetController().SetRedisOffline(true)
		defer chaos.GetController().SetRedisOffline(false)

		// Run outbox worker
		workerCtx, workerCancel := context.WithCancel(ctx)
		go outboxWorker.Start(workerCtx)

		// Allow time for 2 retries and DLQ routing
		time.Sleep(300 * time.Millisecond)
		workerCancel()

		// Verify event was deleted from outbox
		var count int
		_ = database.QueryRowContext(ctx, "SELECT COUNT(*) FROM outbox_events WHERE stream_name = $1", "test-fail-stream").Scan(&count)
		if count != 0 {
			t.Errorf("expected event to be deleted from outbox, got count %d", count)
		}

		// Verify event was routed to dead_letter_events
		var dlqCount int
		_ = database.QueryRowContext(ctx, "SELECT COUNT(*) FROM dead_letter_events WHERE failure_reason LIKE '%failed after%'").Scan(&dlqCount)
		if dlqCount != 1 {
			t.Errorf("expected 1 DLQ event, got %d", dlqCount)
		}
	})
}
