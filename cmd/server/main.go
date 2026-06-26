package main

import (
	"context"
	"crypto/ecdsa"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"offlinepay/internal/api"
	"offlinepay/internal/cluster"
	"offlinepay/internal/config"
	"offlinepay/internal/crypto"
	"offlinepay/internal/db"
	"offlinepay/internal/eventbus"
	"offlinepay/internal/identity"
	"offlinepay/internal/intent"
	"offlinepay/internal/observability"
	"offlinepay/internal/outbox"
	"offlinepay/internal/projection"
	"offlinepay/internal/reconciliation"
	"offlinepay/internal/relay"
	"offlinepay/internal/repository"
	"offlinepay/internal/risk"
	"offlinepay/internal/settlement"
	"offlinepay/internal/token"
	"offlinepay/internal/validator"
)

func main() {
	// 1. Setup Structured Logging wrapped with context correlation ID handler
	baseHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(observability.NewContextHandler(baseHandler))
	slog.SetDefault(logger)

	slog.Info("starting OfflinePay central server...")

	// 2. Load Config
	cfg := config.LoadConfig()

	// 3. Connect to Database (PostgreSQL)
	database, err := db.Connect(cfg.DBURL)
	if err != nil {
		slog.Error("failed to connect to PostgreSQL", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	// 4. Connect to Event Bus (Redis / Mock Redis)
	if cfg.RedisAddr == "localhost:6379" || cfg.RedisAddr == "127.0.0.1:6379" {
		dialer := net.Dialer{Timeout: 1 * time.Second}
		conn, dialErr := dialer.Dial("tcp", cfg.RedisAddr)
		if dialErr != nil {
			slog.Warn("local Redis server not detected; starting in-process mock Redis server...")
			if mockErr := db.StartMockRedisServer("6379"); mockErr != nil {
				slog.Error("failed to start mock Redis server", "error", mockErr)
			} else {
				slog.Info("mock Redis server started successfully on port 6379")
			}
		} else {
			conn.Close()
		}
	}

	eventBus, err := eventbus.NewEventBus(cfg.RedisAddr, cfg.RedisPassword)
	if err != nil {
		slog.Error("failed to connect to Redis", "error", err)
		os.Exit(1)
	}
	defer eventBus.Close()

	// 5. Initialize OpenTelemetry & Metrics
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tp, err := observability.InitTracer(ctx, cfg.Observability.Tracing.ServiceName)
	if err != nil {
		slog.Error("failed to initialize tracer", "error", err)
	}
	defer func() {
		if tp != nil {
			_ = tp.Shutdown(ctx)
		}
	}()

	observability.InitMetrics()
	observability.StartMetricsServer(cfg.Observability.Metrics.Port)

	// 6. Setup Repository & Risk Engine
	repo := repository.NewRepository(database)
	riskEngine := risk.NewRiskEngine(eventBus.GetClient())

	// 7. Load or Generate Bank Key Pair
	var bankPrivateKey *ecdsa.PrivateKey
	if cfg.BankPrivateKeyPEM != "" {
		bankPrivateKey, err = crypto.ParsePEMToPrivateKey(cfg.BankPrivateKeyPEM)
		if err != nil {
			slog.Error("failed to parse configured bank private key", "error", err)
			os.Exit(1)
		}
		slog.Info("loaded configured bank private key")
	} else {
		bankPrivateKey, err = crypto.GenerateKeyPair()
		if err != nil {
			slog.Error("failed to generate bank keypair", "error", err)
			os.Exit(1)
		}
		pemStr, _ := crypto.ExportPrivateKeyToPEM(bankPrivateKey)
		pubStr, _ := crypto.ExportPublicKeyToPEM(&bankPrivateKey.PublicKey)
		slog.Warn("BANK PRIVATE KEY NOT CONFIGURED. Generated keypair for this run:",
			"private_key_pem", pemStr,
			"public_key_pem", pubStr,
		)
	}

	// 8. Instantiate Services
	identitySvc := identity.NewService(repo)
	tokenSvc := token.NewService(repo, bankPrivateKey)
	intentSvc := intent.NewService()
	settleSvc := settlement.NewService(repo, bankPrivateKey, riskEngine)

	// Settle Client for the Relay Service
	settleClient := &api.DirectSettlementClient{SettleSvc: settleSvc}
	relaySvc := relay.NewService(repo, settleClient, "central-relay-node", 5)

	// 9. Start Background Daemon Workers under coordinated WaitGroup
	var wg sync.WaitGroup

	outboxWorker := outbox.NewOutboxWorker(repo, eventBus, 500*time.Millisecond)
	wg.Add(1)
	observability.ActiveWorkers.WithLabelValues("outbox").Set(1)
	go func() {
		defer func() {
			observability.ActiveWorkers.WithLabelValues("outbox").Set(0)
			wg.Done()
		}()
		outboxWorker.Start(ctx)
	}()

	projectionWorker := projection.NewProjectionWorker(repo, eventBus)
	observability.ActiveWorkers.WithLabelValues("projection").Set(1)
	projectionWorker.Start(ctx)

	reconSvc := reconciliation.NewService(repo, tokenSvc, 5*time.Second)
	wg.Add(1)
	observability.ActiveWorkers.WithLabelValues("reconciliation").Set(1)
	go func() {
		defer func() {
			observability.ActiveWorkers.WithLabelValues("reconciliation").Set(0)
			wg.Done()
		}()
		reconSvc.Start(ctx)
	}()

	financialVal := validator.NewFinancialValidator(repo, 5*time.Second)
	wg.Add(1)
	observability.ActiveWorkers.WithLabelValues("validator").Set(1)
	go func() {
		defer func() {
			observability.ActiveWorkers.WithLabelValues("validator").Set(0)
			wg.Done()
		}()
		financialVal.Start(ctx)
	}()

	// 10. Instantiate Cluster Consensus Simulation
	clusterSim := cluster.NewCluster()

	// 11. Start API Server (Gin) using standard net/http
	srv := api.NewServer(cfg, repo, eventBus, identitySvc, tokenSvc, intentSvc, relaySvc, settleSvc)
	srv.SetOutboxWorker(outboxWorker)
	srv.SetCluster(clusterSim)

	// Create channels to handle termination signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start HTTP Server
	httpServer := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: srv.Handler(),
	}

	go func() {
		slog.Info("starting HTTP API Server", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("API Server failed to run", "error", err)
			os.Exit(1)
		}
	}()

	sig := <-sigChan
	slog.Info("received shutdown signal, initiating graceful shutdown", "signal", sig)
	cancel() // Cancel context to signal workers to stop
	observability.ActiveWorkers.WithLabelValues("projection").Set(0)

	// 12. Shutdown HTTP Server gracefully
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("failed to shutdown HTTP server gracefully", "error", err)
	}

	// 13. Wait for background workers to complete cleanly
	workersDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(workersDone)
	}()

	select {
	case <-workersDone:
		slog.Info("all background workers stopped gracefully")
	case <-time.After(5 * time.Second):
		slog.Warn("timed out waiting for background workers to stop")
	}

	slog.Info("OfflinePay shutdown complete")
}
