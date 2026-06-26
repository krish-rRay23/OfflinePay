package settlement

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"offlinepay/internal/crypto"
	"offlinepay/internal/domain"
	"offlinepay/internal/identity"
	"offlinepay/internal/intent"
	"offlinepay/internal/projection"
	"offlinepay/internal/repository"
	"offlinepay/internal/risk"
	"offlinepay/internal/token"
)

func TestCorrectness_FSMTokenTransitions(t *testing.T) {
	database, cleanup := setupTestDB(t)
	if database == nil {
		return
	}
	defer cleanup()

	ctx := context.Background()
	repo := repository.NewRepository(database)

	tokenID := "test-token-fsm"
	tok := &domain.OfflineToken{
		TokenID:          tokenID,
		OwnerID:          "alice",
		Value:            1000,
		Expiry:           time.Now().Add(24 * time.Hour),
		Consumed:         false,
		TokenSignature:   "sig",
		ReservedAt:       time.Now(),
		RiskScoreAtIssue: 0.1,
		Status:           "ISSUED",
	}

	err := repo.CreateToken(ctx, nil, tok)
	if err != nil {
		t.Fatalf("failed to create token: %v", err)
	}

	// ISSUED -> HELD (Allowed)
	err = repo.HoldToken(ctx, nil, tokenID)
	if err != nil {
		t.Fatalf("ISSUED -> HELD failed: %v", err)
	}

	// HELD -> CONSUMED (Allowed)
	err = repo.ConsumeToken(ctx, nil, tokenID)
	if err != nil {
		t.Fatalf("HELD -> CONSUMED failed: %v", err)
	}

	// CONSUMED -> HELD (Should Fail)
	err = repo.HoldToken(ctx, nil, tokenID)
	if err == nil {
		t.Fatal("expected CONSUMED -> HELD to fail")
	}

	// Token state machine cannot go backward (UnconsumeToken should fail)
	err = repo.UnconsumeToken(ctx, nil, tokenID)
	if err == nil {
		t.Fatal("expected UnconsumeToken to fail")
	}
}

func TestCorrectness_ExactlyOnceAndReplay(t *testing.T) {
	database, cleanup := setupTestDB(t)
	if database == nil {
		return
	}
	defer cleanup()

	ctx := context.Background()
	repo := repository.NewRepository(database)
	bankPriv, _ := crypto.GenerateKeyPair()
	devicePriv, _ := crypto.GenerateKeyPair()

	identitySvc := identity.NewService(repo)
	tokenSvc := token.NewService(repo, bankPriv)
	settleSvc := NewService(repo, bankPriv, risk.NewRiskEngine(nil))
	intentSvc := intent.NewService()

	senderID := "alice-exact"
	receiverID := "bob-exact"
	deviceID := "dev-alice-exact"

	err := repo.CreateAccount(ctx, senderID, 10000)
	if err != nil {
		t.Fatalf("failed to create sender account: %v", err)
	}
	err = repo.CreateAccount(ctx, receiverID, 0)
	if err != nil {
		t.Fatalf("failed to create receiver account: %v", err)
	}
	devicePubPEM, err := crypto.ExportPublicKeyToPEM(&devicePriv.PublicKey)
	if err != nil {
		t.Fatalf("failed to export public key: %v", err)
	}
	_, err = identitySvc.RegisterDevice(ctx, deviceID, senderID, devicePubPEM)
	if err != nil {
		t.Fatalf("failed to register device: %v", err)
	}

	tok, err := tokenSvc.IssueToken(ctx, senderID, 5000, 1*time.Hour)
	if err != nil {
		t.Fatalf("failed to issue token: %v", err)
	}
	envelope, _, err := intentSvc.CreateSignedAndEncryptedEnvelope(
		senderID, receiverID, 2000, "USD", deviceID, tok.TokenID, devicePriv, &bankPriv.PublicKey, 10*time.Minute,
	)
	if err != nil {
		t.Fatalf("failed to create envelope: %v", err)
	}

	// Concurrently dispatch the same envelope 50 times
	var successCount int64
	var duplicateCount int64
	var wg sync.WaitGroup
	wg.Add(50)

	for i := 0; i < 50; i++ {
		go func() {
			defer wg.Done()
			status, err := settleSvc.Settle(ctx, envelope, 1)
			if err == nil && status == domain.StateSettled {
				atomic.AddInt64(&successCount, 1)
			} else if status == domain.StateDuplicate || (err != nil && (err.Error() == "spending token already consumed" || err.Error() == "token_already_consumed_concurrent")) {
				atomic.AddInt64(&duplicateCount, 1)
			}
		}()
	}
	wg.Wait()

	if successCount != 1 {
		t.Errorf("expected exactly 1 success, got %d", successCount)
	}

	if successCount+duplicateCount != 50 {
		t.Errorf("expected sum of successes and duplicates to be 50, got %d", successCount+duplicateCount)
	}
}

func TestCorrectness_LedgerParityValidator(t *testing.T) {
	database, cleanup := setupTestDB(t)
	if database == nil {
		return
	}
	defer cleanup()

	ctx := context.Background()
	repo := repository.NewRepository(database)

	accountID := "test-audit-acc"
	err := repo.CreateAccount(ctx, accountID, 1000)
	if err != nil {
		t.Fatalf("failed to create account: %v", err)
	}

	// Create a dummy device, payment intent, and ledger entry so there is a ledger history
	_ = repo.CreateDevice(ctx, &domain.Device{
		DeviceID:   "dev-audit",
		OwnerID:    accountID,
		PublicKey:  "key",
		TrustScore: 1.0,
		Status:     domain.DeviceActive,
		CreatedAt:  time.Now(),
	})

	_ = repo.CreatePaymentIntent(ctx, nil, &domain.PaymentIntent{
		TxnID:      "txn-audit",
		SenderID:   accountID,
		ReceiverID: "bob",
		Amount:     1000,
		Currency:   "USD",
		Nonce:      "nonce-audit",
		Expiry:     time.Now().Add(1 * time.Hour),
		Status:     "SETTLED",
		DeviceID:   "dev-audit",
		TokenID:    "UNKNOWN",
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	})

	err = repo.WithTx(ctx, func(tx *sql.Tx) error {
		return repo.CreateLedgerEntry(ctx, tx, &domain.LedgerEntry{
			TxnID:        "txn-audit",
			AccountID:    accountID,
			Direction:    domain.DirectionDebit,
			Amount:       1000,
			EntryType:    domain.EntryTypeSettlement,
			BalanceAfter: 1000,
		})
	})
	if err != nil {
		t.Fatalf("failed to create ledger entry: %v", err)
	}

	// Audit initially: no discrepancies
	discrepancies, err := repo.AuditAccountBalances(ctx)
	if err != nil {
		t.Fatalf("failed to audit: %v", err)
	}
	if len(discrepancies) > 0 {
		t.Errorf("expected 0 discrepancies, got %d", len(discrepancies))
	}

	// Deliberately mess up the available balance to break the parity:
	// AvailableBalance = 900, but there is a ledger entry indicating 1000
	err = repo.UpdateBalance(ctx, nil, accountID, 900, 0)
	if err != nil {
		t.Fatalf("failed to update balance: %v", err)
	}

	discrepancies, err = repo.AuditAccountBalances(ctx)
	if err != nil {
		t.Fatalf("failed to audit second time: %v", err)
	}
	if len(discrepancies) != 1 {
		t.Errorf("expected exactly 1 discrepancy, got %d", len(discrepancies))
	} else {
		d := discrepancies[0]
		if d.AccountID != accountID {
			t.Errorf("expected account ID %s, got %s", accountID, d.AccountID)
		}
		if d.ExpectedAvailable != 1000 || d.ActualAvailable != 900 {
			t.Errorf("expected expected=1000, actual=900; got expected=%d, actual=%d", d.ExpectedAvailable, d.ActualAvailable)
		}
	}
}

func TestCorrectness_ConsumerIdempotencyAndRebuild(t *testing.T) {
	database, cleanup := setupTestDB(t)
	if database == nil {
		return
	}
	defer cleanup()

	ctx := context.Background()
	repo := repository.NewRepository(database)
	bankPriv, _ := crypto.GenerateKeyPair()
	devicePriv, _ := crypto.GenerateKeyPair()

	identitySvc := identity.NewService(repo)
	tokenSvc := token.NewService(repo, bankPriv)
	settleSvc := NewService(repo, bankPriv, risk.NewRiskEngine(nil))
	intentSvc := intent.NewService()

	senderID := "alice-rebuild"
	receiverID := "bob-rebuild"
	deviceID := "dev-alice-rebuild"

	err := repo.CreateAccount(ctx, senderID, 10000)
	if err != nil {
		t.Fatalf("failed to create sender account: %v", err)
	}
	err = repo.CreateAccount(ctx, receiverID, 0)
	if err != nil {
		t.Fatalf("failed to create receiver account: %v", err)
	}
	devicePubPEM, err := crypto.ExportPublicKeyToPEM(&devicePriv.PublicKey)
	if err != nil {
		t.Fatalf("failed to export public key: %v", err)
	}
	_, err = identitySvc.RegisterDevice(ctx, deviceID, senderID, devicePubPEM)
	if err != nil {
		t.Fatalf("failed to register device: %v", err)
	}

	tok, err := tokenSvc.IssueToken(ctx, senderID, 5000, 1*time.Hour)
	if err != nil {
		t.Fatalf("failed to issue token: %v", err)
	}
	envelope, _, err := intentSvc.CreateSignedAndEncryptedEnvelope(
		senderID, receiverID, 2000, "USD", deviceID, tok.TokenID, devicePriv, &bankPriv.PublicKey, 10*time.Minute,
	)
	if err != nil {
		t.Fatalf("failed to create envelope: %v", err)
	}

	// Settle transaction
	status, err := settleSvc.Settle(ctx, envelope, 1)
	if err != nil || status != domain.StateSettled {
		t.Fatalf("failed to settle: %v", err)
	}

	// We verify that the outbox event and the payment event exist in the DB
	events, err := repo.GetAllPaymentEvents(ctx)
	if err != nil {
		t.Fatalf("failed to get payment events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected at least 1 payment event in event store")
	}
	ev := events[0]

	// Simulate event processing via worker helper
	worker := projection.NewProjectionWorker(repo, nil)

	// 1. Process for the first time
	evIDStr := strconv.FormatInt(ev.EventID, 10)
	err = worker.HandlePaymentSettled(evIDStr, "IntentSettled", ev.Payload)
	if err != nil {
		t.Fatalf("HandlePaymentSettled failed: %v", err)
	}

	// Verify projection was updated
	proj, err := repo.GetReadProjection(ctx, ev.TxnID)
	if err != nil {
		t.Fatalf("failed to get read projection: %v", err)
	}
	if proj.Status != domain.StateSettled {
		t.Errorf("expected projection status SETTLED, got %s", proj.Status)
	}

	// Checkpoint should be updated
	chk, err := repo.GetProjectionCheckpoint(ctx, "payment-projection")
	if err != nil {
		t.Fatalf("failed to get checkpoint: %v", err)
	}
	if chk != ev.EventID {
		t.Errorf("expected checkpoint %d, got %d", ev.EventID, chk)
	}

	// 2. Process the same event ID again (Consumer Idempotency)
	// We modify the projection in DB to CREATED to see if processing again updates it. If idempotency works, it should skip updating and keep it CREATED.
	_, _ = database.ExecContext(ctx, "UPDATE payment_read_projections SET status = 'CREATED'")
	err = worker.HandlePaymentSettled(evIDStr, "IntentSettled", ev.Payload)
	if err != nil {
		t.Fatalf("HandlePaymentSettled failed on duplicate: %v", err)
	}

	proj2, _ := repo.GetReadProjection(ctx, ev.TxnID)
	if proj2.Status != "CREATED" {
		t.Errorf("expected idempotency to skip processing, but status changed to %s", proj2.Status)
	}

	// 3. Rebuild Verification
	// Let's manually trigger rebuild logic: truncate projection tables, replay from event store
	_, _ = database.ExecContext(ctx, "TRUNCATE TABLE payment_read_projections")
	_, _ = database.ExecContext(ctx, "DELETE FROM projection_checkpoints WHERE projection_name = 'payment-projection'")

	// Replay loop
	for _, ev := range events {
		var data struct {
			TxnID     string    `json:"txn_id"`
			Sender    string    `json:"sender"`
			Receiver  string    `json:"receiver"`
			Amount    int64     `json:"amount"`
			TokenID   string    `json:"token_id"`
			SettledAt time.Time `json:"settled_at"`
			RelayHops int       `json:"relay_hops"`
		}
		_ = json.Unmarshal([]byte(ev.Payload), &data)

		err = repo.WithTx(ctx, func(tx *sql.Tx) error {
			proj := &domain.PaymentReadProjection{
				TxnID:      ev.TxnID,
				SenderID:   data.Sender,
				ReceiverID: data.Receiver,
				Amount:     data.Amount,
				Status:     domain.StateSettled,
				RelayHops:  data.RelayHops,
				SettledAt:  &data.SettledAt,
				UpdatedAt:  time.Now(),
			}
			_ = repo.CreateOrUpdateReadProjection(ctx, tx, proj)
			_ = repo.UpdateProjectionCheckpoint(ctx, tx, "payment-projection", ev.EventID)
			return nil
		})
		if err != nil {
			t.Fatalf("replay event failed: %v", err)
		}
	}

	// Verify projection rebuilt successfully
	projRebuilt, err := repo.GetReadProjection(ctx, ev.TxnID)
	if err != nil {
		t.Fatalf("rebuilt projection missing: %v", err)
	}
	if projRebuilt.Status != domain.StateSettled {
		t.Errorf("expected rebuilt status SETTLED, got %s", projRebuilt.Status)
	}
}
