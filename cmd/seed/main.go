package main

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"log/slog"
	"os"
	"time"

	"offlinepay/internal/config"
	"offlinepay/internal/crypto"
	"offlinepay/internal/db"
	"offlinepay/internal/domain"
	"offlinepay/internal/repository"
)

func main() {
	slog.Info("running database seeder...")

	// 1. Load config
	cfg := config.LoadConfig()

	// 2. Connect to database
	database, err := db.Connect(cfg.DBURL)
	if err != nil {
		slog.Error("failed to connect to database for seeding", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	repo := repository.NewRepository(database)
	ctx := context.Background()

	// 3. Clear existing balances/devices/tokens to make seeding clean and idempotent
	slog.Info("clearing existing tables to prepare fresh seed state...")
	_, _ = database.ExecContext(ctx, "DELETE FROM ledger_entries")
	_, _ = database.ExecContext(ctx, "DELETE FROM nonce_registry")
	_, _ = database.ExecContext(ctx, "DELETE FROM payment_intents")
	_, _ = database.ExecContext(ctx, "DELETE FROM offline_tokens")
	_, _ = database.ExecContext(ctx, "DELETE FROM devices")
	_, _ = database.ExecContext(ctx, "DELETE FROM account_balances")

	// 4. Seed Account Balances
	slog.Info("seeding account balances...")
	accounts := []struct {
		id        string
		available int64
		reserved  int64
	}{
		{"user-alice", 5000, 5000}, // $50 available, $50 reserved for offline token
		{"user-bob", 10000, 0},     // $100 available
		{"merchant-charlie", 0, 0}, // merchant starting fresh
		{"sender-storm", 100000, 0}, // used for high concurrency simulations
		{"receiver-storm", 0, 0},
	}

	for _, acc := range accounts {
		err := repo.CreateAccount(ctx, acc.id, acc.available)
		if err != nil {
			slog.Error("failed to create account", "id", acc.id, "error", err)
			os.Exit(1)
		}
		if acc.reserved > 0 {
			_, err = database.ExecContext(ctx, "UPDATE account_balances SET reserved_balance = $1 WHERE account_id = $2", acc.reserved, acc.id)
			if err != nil {
				slog.Error("failed to set reserved balance", "id", acc.id, "error", err)
				os.Exit(1)
			}
		}
		slog.Info("account created", "id", acc.id, "available", acc.available, "reserved", acc.reserved)
	}

	// 5. Seed Device Identities
	slog.Info("seeding trusted hardware devices...")
	// Generate key pairs for seeded devices
	aliceDevPriv, _ := crypto.GenerateKeyPair()
	aliceDevPubStr, _ := crypto.ExportPublicKeyToPEM(&aliceDevPriv.PublicKey)
	aliceDevPrivStr, _ := crypto.ExportPrivateKeyToPEM(aliceDevPriv)

	bobDevPriv, _ := crypto.GenerateKeyPair()
	bobDevPubStr, _ := crypto.ExportPublicKeyToPEM(&bobDevPriv.PublicKey)
	bobDevPrivStr, _ := crypto.ExportPrivateKeyToPEM(bobDevPriv)

	devices := []*domain.Device{
		{
			DeviceID:   "dev-alice-phone",
			OwnerID:    "user-alice",
			PublicKey:  aliceDevPubStr,
			TrustScore: 0.98,
			Status:     domain.DeviceActive,
			CreatedAt:  time.Now(),
		},
		{
			DeviceID:   "dev-bob-phone",
			OwnerID:    "user-bob",
			PublicKey:  bobDevPubStr,
			TrustScore: 0.90,
			Status:     domain.DeviceActive,
			CreatedAt:  time.Now(),
		},
	}

	for _, dev := range devices {
		err := repo.CreateDevice(ctx, dev)
		if err != nil {
			slog.Error("failed to register device", "id", dev.DeviceID, "error", err)
			os.Exit(1)
		}
		slog.Info("registered trusted device", "device_id", dev.DeviceID, "owner", dev.OwnerID, "trust", dev.TrustScore)
	}

	// 6. Generate Bank Private Key & Seed Signed Offline Token for Alice
	slog.Info("seeding bank-signed offline spending token...")
	var bankPriv *ecdsa.PrivateKey
	if cfg.BankPrivateKeyPEM != "" {
		bankPriv, _ = crypto.ParsePEMToPrivateKey(cfg.BankPrivateKeyPEM)
	} else {
		bankPriv, _ = crypto.GenerateKeyPair()
	}

	tokenID := "token-alice-50"
	tokenValue := int64(5000) // $50
	tokenExpiry := time.Now().Add(365 * 24 * time.Hour) // 1 year expiry

	tokenData := fmt.Sprintf("token:%s:owner:%s:value:%d:expiry:%d", tokenID, "user-alice", tokenValue, tokenExpiry.Unix())
	signature, err := crypto.Sign(bankPriv, []byte(tokenData))
	if err != nil {
		slog.Error("failed to sign seeded offline token", "error", err)
		os.Exit(1)
	}

	tok := &domain.OfflineToken{
		TokenID:          tokenID,
		OwnerID:          "user-alice",
		Value:            tokenValue,
		Expiry:           tokenExpiry,
		Consumed:         false,
		TokenSignature:   signature,
		ReservedAt:       time.Now(),
		RiskScoreAtIssue: 0.05,
		Status:           "ISSUED",
	}

	err = repo.CreateToken(ctx, nil, tok)
	if err != nil {
		slog.Error("failed to seed offline token", "id", tokenID, "error", err)
		os.Exit(1)
	}
	slog.Info("seeded offline spending token", "token_id", tokenID, "value_cents", tokenValue, "owner", "user-alice")

	// Print useful configuration instructions
	bankPrivPEM, _ := crypto.ExportPrivateKeyToPEM(bankPriv)
	bankPubPEM, _ := crypto.ExportPublicKeyToPEM(&bankPriv.PublicKey)

	fmt.Println("\n================================================================================")
	fmt.Println("DATABASE SEEDING COMPLETED SUCCESSFULLY!")
	fmt.Println("================================================================================")
	fmt.Println("Use the following credentials and keys for testing:")
	fmt.Printf("\n- Sender Account: user-alice (device: dev-alice-phone, balance: $50 avail / $50 reserved)\n")
	fmt.Printf("- Seeded Token: token-alice-50 ($50 offline spending allowance)\n")
	fmt.Printf("- Receiver Account: merchant-charlie\n")
	fmt.Printf("\n- Ephemeral Bank Public Key:\n%s\n", bankPubPEM)
	fmt.Printf("- Ephemeral Bank Private Key (Set BANK_PRIVATE_KEY env var to keep this dynamic key):\n%s\n", bankPrivPEM)
	fmt.Printf("- Alice Device Private Key:\n%s\n", aliceDevPrivStr)
	fmt.Printf("- Bob Device Private Key:\n%s\n", bobDevPrivStr)
	fmt.Println("================================================================================")
}
