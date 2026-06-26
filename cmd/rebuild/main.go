package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"offlinepay/internal/config"
	"offlinepay/internal/db"
	"offlinepay/internal/domain"
	"offlinepay/internal/repository"
)

func main() {
	// Setup logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	slog.Info("starting CQRS Read Projection Rebuild Engine...")

	// Load config
	cfg := config.LoadConfig()

	// Connect to Database
	database, err := db.Connect(cfg.DBURL)
	if err != nil {
		slog.Error("failed to connect to PostgreSQL", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	repo := repository.NewRepository(database)
	ctx := context.Background()

	// 1. Fetch current projections to cache in memory
	slog.Info("caching existing read projections in memory...")
	queryProjections := `SELECT txn_id, sender_id, receiver_id, amount, status, relay_hops, settled_at, updated_at FROM payment_read_projections`
	rows, err := database.QueryContext(ctx, queryProjections)
	if err != nil {
		slog.Error("failed to query existing read projections", "error", err)
		os.Exit(1)
	}

	cachedProjections := make(map[string]*domain.PaymentReadProjection)
	for rows.Next() {
		var p domain.PaymentReadProjection
		err := rows.Scan(&p.TxnID, &p.SenderID, &p.ReceiverID, &p.Amount, &p.Status, &p.RelayHops, &p.SettledAt, &p.UpdatedAt)
		if err != nil {
			slog.Error("failed to scan projection", "error", err)
			os.Exit(1)
		}
		cachedProjections[p.TxnID] = &p
	}
	rows.Close()
	slog.Info("cached projections loaded", "count", len(cachedProjections))

	// 2. Truncate payment_read_projections and projection_checkpoints
	slog.Info("truncating payment_read_projections and projection_checkpoints tables...")
	_, err = database.ExecContext(ctx, "TRUNCATE TABLE payment_read_projections")
	if err != nil {
		slog.Error("failed to truncate payment_read_projections", "error", err)
		os.Exit(1)
	}
	_, err = database.ExecContext(ctx, "DELETE FROM projection_checkpoints WHERE projection_name = 'payment-projection'")
	if err != nil {
		slog.Error("failed to delete projection checkpoints", "error", err)
		os.Exit(1)
	}

	// 3. Fetch all payment events
	slog.Info("retrieving all payment events from event store...")
	events, err := repo.GetAllPaymentEvents(ctx)
	if err != nil {
		slog.Error("failed to retrieve payment events", "error", err)
		os.Exit(1)
	}
	slog.Info("retrieved events", "count", len(events))

	// 4. Replay events in chronological order to reconstruct read projections
	var maxEventID int64
	for _, ev := range events {
		slog.Info("replaying event", "event_id", ev.EventID, "txn_id", ev.TxnID, "type", ev.EventType)

		if ev.EventID > maxEventID {
			maxEventID = ev.EventID
		}

		err = repo.WithTx(ctx, func(tx *sql.Tx) error {
			// Retrieve existing read projection if any to keep sender/receiver/amount
			proj, err := repo.GetReadProjection(ctx, ev.TxnID)
			if err != nil {
				proj = &domain.PaymentReadProjection{
					TxnID:      ev.TxnID,
					SenderID:   "UNKNOWN",
					ReceiverID: "UNKNOWN",
					Amount:     0,
				}
			}

			switch ev.EventType {
			case "IntentSettled":
				var data struct {
					TxnID     string    `json:"txn_id"`
					Sender    string    `json:"sender"`
					Receiver  string    `json:"receiver"`
					Amount    int64     `json:"amount"`
					TokenID   string    `json:"token_id"`
					SettledAt time.Time `json:"settled_at"`
					RelayHops int       `json:"relay_hops"`
				}
				if err := json.Unmarshal([]byte(ev.Payload), &data); err == nil {
					proj.SenderID = data.Sender
					proj.ReceiverID = data.Receiver
					proj.Amount = data.Amount
					proj.Status = domain.StateSettled
					proj.RelayHops = data.RelayHops
					proj.SettledAt = &data.SettledAt
				} else {
					slog.Warn("failed to parse IntentSettled payload during replay", "error", err)
				}
			case "IntentFailed":
				var data struct {
					Status    string `json:"status"`
					Reason    string `json:"reason"`
					RelayHops int    `json:"relay_hops"`
				}
				if err := json.Unmarshal([]byte(ev.Payload), &data); err == nil {
					proj.Status = data.Status
					proj.RelayHops = data.RelayHops
				} else {
					slog.Warn("failed to parse IntentFailed payload during replay", "error", err)
				}
			default:
				slog.Warn("skipping unrecognized event type", "type", ev.EventType)
				return nil
			}

			proj.UpdatedAt = time.Now()

			// Update Read Projection table
			err = repo.CreateOrUpdateReadProjection(ctx, tx, proj)
			if err != nil {
				return err
			}

			// Update projection checkpoint
			err = repo.UpdateProjectionCheckpoint(ctx, tx, "payment-projection", ev.EventID)
			if err != nil {
				return err
			}

			return nil
		})

		if err != nil {
			slog.Error("failed to process event during replay", "event_id", ev.EventID, "error", err)
			os.Exit(1)
		}
	}

	// 5. Query the newly reconstructed projections and verify correctness
	slog.Info("verifying reconstructed read projections...")
	rows, err = database.QueryContext(ctx, queryProjections)
	if err != nil {
		slog.Error("failed to query reconstructed projections", "error", err)
		os.Exit(1)
	}

	reconstructedProjections := make(map[string]*domain.PaymentReadProjection)
	for rows.Next() {
		var p domain.PaymentReadProjection
		err := rows.Scan(&p.TxnID, &p.SenderID, &p.ReceiverID, &p.Amount, &p.Status, &p.RelayHops, &p.SettledAt, &p.UpdatedAt)
		if err != nil {
			slog.Error("failed to scan reconstructed projection", "error", err)
			os.Exit(1)
		}
		reconstructedProjections[p.TxnID] = &p
	}
	rows.Close()

	if len(cachedProjections) == 0 {
		slog.Info("Warning: Cached projections table was empty. Rebuilt projections from scratch. Skipping mismatch validation.")
	} else if len(cachedProjections) != len(reconstructedProjections) {
		slog.Error("verification failed: projection count mismatch", "cached", len(cachedProjections), "reconstructed", len(reconstructedProjections))
		os.Exit(1)
	}

	for txnID, cachedProj := range cachedProjections {
		reconProj, exists := reconstructedProjections[txnID]
		if !exists {
			slog.Error("verification failed: transaction missing in reconstructed database", "txn_id", txnID)
			os.Exit(1)
		}
		if cachedProj.Status != reconProj.Status {
			slog.Error("verification failed: status mismatch", "txn_id", txnID, "expected", cachedProj.Status, "actual", reconProj.Status)
			os.Exit(1)
		}
		if cachedProj.Amount != reconProj.Amount {
			slog.Error("verification failed: amount mismatch", "txn_id", txnID, "expected", cachedProj.Amount, "actual", reconProj.Amount)
			os.Exit(1)
		}
		if cachedProj.SenderID != reconProj.SenderID {
			slog.Error("verification failed: sender mismatch", "txn_id", txnID, "expected", cachedProj.SenderID, "actual", reconProj.SenderID)
			os.Exit(1)
		}
		if cachedProj.ReceiverID != reconProj.ReceiverID {
			slog.Error("verification failed: receiver mismatch", "txn_id", txnID, "expected", cachedProj.ReceiverID, "actual", reconProj.ReceiverID)
			os.Exit(1)
		}
		if cachedProj.RelayHops != reconProj.RelayHops {
			slog.Error("verification failed: relay hops mismatch", "txn_id", txnID, "expected", cachedProj.RelayHops, "actual", reconProj.RelayHops)
			os.Exit(1)
		}
	}

	// Verify that checkpoint matches the maximum event ID processed
	checkpointVal, err := repo.GetProjectionCheckpoint(ctx, "payment-projection")
	if err != nil {
		slog.Error("failed to query final checkpoint", "error", err)
		os.Exit(1)
	}
	if checkpointVal != maxEventID {
		slog.Error("verification failed: checkpoint ID mismatch", "expected", maxEventID, "actual", checkpointVal)
		os.Exit(1)
	}

	slog.Info("CQRS read projection rebuild and validation completed successfully. All projections verified!")
	fmt.Println("SUCCESS")
}
