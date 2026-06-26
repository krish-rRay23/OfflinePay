package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/pprof"
	"strconv"
	"time"

	"offlinepay/internal/cluster"
	"offlinepay/internal/config"
	"offlinepay/internal/crypto"
	"offlinepay/internal/domain"
	"offlinepay/internal/eventbus"
	"offlinepay/internal/identity"
	"offlinepay/internal/intent"
	"offlinepay/internal/outbox"
	"offlinepay/internal/relay"
	"offlinepay/internal/repository"
	"offlinepay/internal/settlement"
	"offlinepay/internal/token"

	"github.com/gin-gonic/gin"
)

type Server struct {
	router       *gin.Engine
	identitySvc  *identity.Service
	tokenSvc     *token.Service
	intentSvc    *intent.Service
	relaySvc     *relay.Service
	settleSvc    *settlement.Service
	repo         *repository.Repository
	outboxWorker *outbox.OutboxWorker
	cluster      *cluster.Cluster
	eventBus     *eventbus.EventBus
	cfg          *config.Config
}

func NewServer(
	cfg *config.Config,
	repo *repository.Repository,
	eventBus *eventbus.EventBus,
	idSvc *identity.Service,
	tokSvc *token.Service,
	intSvc *intent.Service,
	relSvc *relay.Service,
	setSvc *settlement.Service,
) *Server {
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	// Serve Static Frontend Assets using NoRoute fallback to avoid root wildcard conflicts in Gin
	r.NoRoute(gin.WrapH(http.FileServer(http.Dir("./web"))))

	// Serve OpenAPI Spec and Swagger UI
	r.StaticFile("/openapi.yaml", "./openapi.yaml")
	r.GET("/swagger", serveSwaggerUI)

	s := &Server{
		router:      r,
		identitySvc: idSvc,
		tokenSvc:    tokSvc,
		intentSvc:   intSvc,
		relaySvc:    relSvc,
		settleSvc:   setSvc,
		repo:        repo,
		eventBus:    eventBus,
		cfg:         cfg,
	}

	// Register debug pprof endpoints
	registerPprof(r)

	// Register health endpoints (unauthenticated, un-rate-limited)
	r.GET("/health", s.handleHealth)
	r.GET("/live", s.handleLive)
	r.GET("/ready", s.handleReady)

	s.setupRoutes()
	return s
}

func (s *Server) SetOutboxWorker(w *outbox.OutboxWorker) {
	s.outboxWorker = w
}

func (s *Server) SetCluster(c *cluster.Cluster) {
	s.cluster = c
}

func (s *Server) Handler() http.Handler {
	return s.router
}

func (s *Server) setupRoutes() {
	// Initialize rate limiters
	anonLimiter := NewIPRateLimiter(float64(s.cfg.Security.RateLimits.Anonymous), float64(s.cfg.Security.RateLimits.Anonymous))
	authLimiter := NewIPRateLimiter(float64(s.cfg.Security.RateLimits.Authenticated), float64(s.cfg.Security.RateLimits.Authenticated))
	internalLimiter := NewIPRateLimiter(float64(s.cfg.Security.RateLimits.Internal), float64(s.cfg.Security.RateLimits.Internal))

	// Version 1 API group with trace logging and rate limiters
	v1 := s.router.Group("/api/v1")
	v1.Use(TraceAndLoggerMiddleware())
	v1.Use(RateLimitMiddleware(anonLimiter, authLimiter, internalLimiter))
	{
		// Identity endpoints
		v1.POST("/identity/devices", s.handleRegisterDevice)
		v1.GET("/identity/devices/:id", s.handleLookupDevice)
		v1.POST("/identity/devices/:id/revoke", s.handleRevokeDevice)

		// Token endpoints
		v1.POST("/tokens/issue", s.handleIssueToken)

		// Intent endpoints
		v1.POST("/intents/create", s.handleCreateIntent)

		// Relay packet endpoint
		v1.POST("/relays/packet", s.handleRelayPacket)

		// Settlement endpoint
		v1.POST("/settlement/settle", s.handleSettle)

		// Account endpoints (standardized to /accounts instead of /accounts/create)
		v1.POST("/accounts", s.handleCreateAccount)
		v1.GET("/accounts/:id/balance", s.handleGetBalance)

		// Attestation endpoints
		v1.POST("/identity/attestations", s.handleRegisterAttestation)
		v1.GET("/identity/attestations/:device_id", s.handleGetAttestation)

		// DLQ endpoints
		v1.GET("/dlq", s.handleListDLQ)
		v1.POST("/dlq/replay", s.handleReplayDLQ)
		v1.DELETE("/dlq/:id", s.handleDeleteDLQ)

		// Cluster endpoints
		v1.GET("/cluster/health", s.handleClusterHealth)
		v1.POST("/cluster/nodes/:id/crash", s.handleClusterCrashNode)
		v1.POST("/cluster/nodes/:id/recover", s.handleClusterRecoverNode)
	}
}

// Health check endpoints

func (s *Server) handleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "UP"})
}

func (s *Server) handleLive(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "UP"})
}

func (s *Server) handleReady(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	// 1. Check database connectivity
	if err := s.repo.Ping(ctx); err != nil {
		RespondWithError(c, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE", "Postgres database connectivity check failed")
		return
	}

	// 2. Check Redis connection
	if s.eventBus != nil && s.eventBus.GetClient() != nil {
		if err := s.eventBus.GetClient().Ping(ctx).Err(); err != nil {
			RespondWithError(c, http.StatusServiceUnavailable, "REDIS_UNAVAILABLE", "Redis/mock stream connectivity check failed")
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"status": "UP",
		"checks": gin.H{
			"database": "UP",
			"redis":    "UP",
		},
	})
}

// Handler implementations

type RegisterDeviceReq struct {
	DeviceID  string `json:"device_id" binding:"required"`
	OwnerID   string `json:"owner_id" binding:"required"`
	PublicKey string `json:"public_key" binding:"required"`
}

func (s *Server) handleRegisterDevice(c *gin.Context) {
	var req RegisterDeviceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondWithError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}

	dev, err := s.identitySvc.RegisterDevice(c.Request.Context(), req.DeviceID, req.OwnerID, req.PublicKey)
	if err != nil {
		RespondWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	c.JSON(http.StatusOK, dev)
}

func (s *Server) handleLookupDevice(c *gin.Context) {
	id := c.Param("id")
	dev, err := s.identitySvc.LookupDevice(c.Request.Context(), id)
	if err != nil {
		RespondWithError(c, http.StatusNotFound, "DEVICE_NOT_FOUND", "device not found")
		return
	}
	c.JSON(http.StatusOK, dev)
}

type RevokeDeviceReq struct {
	Compromised bool `json:"compromised"`
}

func (s *Server) handleRevokeDevice(c *gin.Context) {
	id := c.Param("id")
	var req RevokeDeviceReq
	_ = c.ShouldBindJSON(&req) // Optional compromised flag

	err := s.identitySvc.RevokeDevice(c.Request.Context(), id, req.Compromised)
	if err != nil {
		RespondWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "revoked"})
}

type IssueTokenReq struct {
	OwnerID         string `json:"owner_id" binding:"required"`
	Value           int64  `json:"value" binding:"required"`
	DurationSeconds int    `json:"duration_seconds" binding:"required"`
}

func (s *Server) handleIssueToken(c *gin.Context) {
	var req IssueTokenReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondWithError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}

	tok, err := s.tokenSvc.IssueToken(
		c.Request.Context(),
		req.OwnerID,
		req.Value,
		time.Duration(req.DurationSeconds)*time.Second,
	)
	if err != nil {
		RespondWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	c.JSON(http.StatusOK, tok)
}

type CreateIntentReq struct {
	SenderID            string `json:"sender_id" binding:"required"`
	ReceiverID          string `json:"receiver_id" binding:"required"`
	Amount              int64  `json:"amount" binding:"required"`
	Currency            string `json:"currency" binding:"required"`
	DeviceID            string `json:"device_id" binding:"required"`
	TokenID             string `json:"token_id" binding:"required"`
	DevicePrivateKeyPEM string `json:"device_private_key_pem" binding:"required"`
	BankPublicKeyPEM    string `json:"bank_public_key_pem" binding:"required"`
	DurationSeconds     int    `json:"duration_seconds" binding:"required"`
}

func (s *Server) handleCreateIntent(c *gin.Context) {
	var req CreateIntentReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondWithError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}

	devPriv, err := crypto.ParsePEMToPrivateKey(req.DevicePrivateKeyPEM)
	if err != nil {
		RespondWithError(c, http.StatusBadRequest, "INVALID_KEY", "invalid device private key PEM")
		return
	}

	bankPub, err := crypto.ParsePEMToPublicKey(req.BankPublicKeyPEM)
	if err != nil {
		RespondWithError(c, http.StatusBadRequest, "INVALID_KEY", "invalid bank public key PEM")
		return
	}

	envelope, payload, err := s.intentSvc.CreateSignedAndEncryptedEnvelope(
		req.SenderID,
		req.ReceiverID,
		req.Amount,
		req.Currency,
		req.DeviceID,
		req.TokenID,
		devPriv,
		bankPub,
		time.Duration(req.DurationSeconds)*time.Second,
	)
	if err != nil {
		RespondWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"envelope": envelope,
		"payload":  payload,
	})
}

type RelayPacketReq struct {
	Envelope domain.EncryptedEnvelope `json:"envelope" binding:"required"`
	HopCount int                      `json:"hop_count"`
}

func (s *Server) handleRelayPacket(c *gin.Context) {
	var req RelayPacketReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondWithError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}

	err := s.relaySvc.ReceiveAndRelay(c.Request.Context(), &req.Envelope, req.HopCount)
	if err != nil {
		RespondWithError(c, http.StatusBadRequest, "RELAY_FAILED", err.Error())
		return
	}

	c.JSON(http.StatusAccepted, gin.H{"status": "relaying"})
}

type SettleReq struct {
	Envelope domain.EncryptedEnvelope `json:"envelope" binding:"required"`
	HopCount int                      `json:"hop_count"`
}

func (s *Server) handleSettle(c *gin.Context) {
	var req SettleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondWithError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}

	status, err := s.settleSvc.Settle(c.Request.Context(), &req.Envelope, req.HopCount)
	if err != nil {
		RespondWithError(c, http.StatusUnprocessableEntity, "SETTLEMENT_FAILED", err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": status})
}

type CreateAccountReq struct {
	AccountID      string `json:"account_id" binding:"required"`
	InitialBalance int64  `json:"initial_balance"`
}

func (s *Server) handleCreateAccount(c *gin.Context) {
	var req CreateAccountReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondWithError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}

	err := s.repo.CreateAccount(c.Request.Context(), req.AccountID, req.InitialBalance)
	if err != nil {
		RespondWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "account_created"})
}

func (s *Server) handleGetBalance(c *gin.Context) {
	id := c.Param("id")
	bal, err := s.repo.GetBalance(c.Request.Context(), id)
	if err != nil {
		RespondWithError(c, http.StatusNotFound, "ACCOUNT_NOT_FOUND", "account not found")
		return
	}
	c.JSON(http.StatusOK, bal)
}

type DirectSettlementClient struct {
	SettleSvc *settlement.Service
}

func (dsc *DirectSettlementClient) Settle(ctx context.Context, env *domain.EncryptedEnvelope, hopCount int) (string, error) {
	time.Sleep(10 * time.Millisecond)
	return dsc.SettleSvc.Settle(ctx, env, hopCount)
}

type HTTPSettlementClient struct {
	url string
}

func NewHTTPSettlementClient(url string) *HTTPSettlementClient {
	return &HTTPSettlementClient{url: url}
}

func (hsc *HTTPSettlementClient) Settle(ctx context.Context, env *domain.EncryptedEnvelope, hopCount int) (string, error) {
	return "", errors.New("http settlement client not fully configured")
}

type RegisterAttestationReq struct {
	DeviceID        string `json:"device_id" binding:"required"`
	AttestationType string `json:"attestation_type" binding:"required"`
	AttestationHash string `json:"attestation_hash" binding:"required"`
	TrustLevel      string `json:"trust_level" binding:"required"`
}

func (s *Server) handleRegisterAttestation(c *gin.Context) {
	var req RegisterAttestationReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondWithError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}

	att := &domain.DeviceAttestation{
		DeviceID:        req.DeviceID,
		AttestationType: req.AttestationType,
		AttestationHash: req.AttestationHash,
		TrustLevel:      req.TrustLevel,
		CreatedAt:       time.Now(),
	}

	err := s.repo.CreateDeviceAttestation(c.Request.Context(), att)
	if err != nil {
		RespondWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	c.JSON(http.StatusOK, att)
}

func (s *Server) handleGetAttestation(c *gin.Context) {
	deviceID := c.Param("device_id")
	att, err := s.repo.GetDeviceAttestation(c.Request.Context(), deviceID)
	if err != nil {
		RespondWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	if att == nil {
		RespondWithError(c, http.StatusNotFound, "ATTESTATION_NOT_FOUND", "attestation not found")
		return
	}
	c.JSON(http.StatusOK, att)
}

func (s *Server) handleListDLQ(c *gin.Context) {
	limitStr := c.DefaultQuery("limit", "20")
	offsetStr := c.DefaultQuery("offset", "0")

	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 {
		limit = 20
	}
	offset, err := strconv.Atoi(offsetStr)
	if err != nil || offset < 0 {
		offset = 0
	}

	events, total, err := s.repo.GetDeadLetterEventsPaginated(c.Request.Context(), limit, offset)
	if err != nil {
		RespondWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data": events,
		"pagination": gin.H{
			"total":  total,
			"limit":  limit,
			"offset": offset,
		},
	})
}

type ReplayDLQReq struct {
	EventID int64 `json:"event_id" binding:"required"`
}

func (s *Server) handleReplayDLQ(c *gin.Context) {
	var req ReplayDLQReq
	if err := c.ShouldBindJSON(&req); err != nil {
		RespondWithError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}

	if s.outboxWorker == nil {
		RespondWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "outbox worker not configured on server")
		return
	}

	err := s.outboxWorker.ReplayDLQEvent(c.Request.Context(), req.EventID)
	if err != nil {
		RespondWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "replayed"})
}

func (s *Server) handleDeleteDLQ(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		RespondWithError(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid event id")
		return
	}

	err = s.repo.DeleteDeadLetterEvent(c.Request.Context(), id)
	if err != nil {
		RespondWithError(c, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func (s *Server) handleClusterHealth(c *gin.Context) {
	if s.cluster == nil {
		RespondWithError(c, http.StatusInternalServerError, "CLUSTER_NOT_CONFIGURED", "cluster simulation not configured")
		return
	}
	status := s.cluster.GetStatus()
	c.JSON(http.StatusOK, status)
}

func (s *Server) handleClusterCrashNode(c *gin.Context) {
	if s.cluster == nil {
		RespondWithError(c, http.StatusInternalServerError, "CLUSTER_NOT_CONFIGURED", "cluster simulation not configured")
		return
	}
	id := c.Param("id")
	s.cluster.CrashNode(id)
	c.JSON(http.StatusOK, gin.H{"status": "node crashed", "node_id": id})
}

func (s *Server) handleClusterRecoverNode(c *gin.Context) {
	if s.cluster == nil {
		RespondWithError(c, http.StatusInternalServerError, "CLUSTER_NOT_CONFIGURED", "cluster simulation not configured")
		return
	}
	id := c.Param("id")
	s.cluster.RecoverNode(id)
	c.JSON(http.StatusOK, gin.H{"status": "node recovered", "node_id": id})
}

// registerPprof mounts standard Go diagnostic profiling endpoints on Gin
func registerPprof(r *gin.Engine) {
	rg := r.Group("/debug/pprof")
	rg.GET("/", gin.WrapF(pprof.Index))
	rg.GET("/cmdline", gin.WrapF(pprof.Cmdline))
	rg.GET("/profile", gin.WrapF(pprof.Profile))
	rg.GET("/symbol", gin.WrapF(pprof.Symbol))
	rg.GET("/trace", gin.WrapF(pprof.Trace))
	rg.GET("/allocs", gin.WrapH(pprof.Handler("allocs")))
	rg.GET("/block", gin.WrapH(pprof.Handler("block")))
	rg.GET("/goroutine", gin.WrapH(pprof.Handler("goroutine")))
	rg.GET("/heap", gin.WrapH(pprof.Handler("heap")))
	rg.GET("/mutex", gin.WrapH(pprof.Handler("mutex")))
	rg.GET("/threadcreate", gin.WrapH(pprof.Handler("threadcreate")))
}

// serveSwaggerUI returns a self-contained HTML page loading Swagger UI from unpkg/cdnjs CDN
func serveSwaggerUI(c *gin.Context) {
	swaggerUIHTML := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>OfflinePay API Reference</title>
    <link rel="stylesheet" type="text/css" href="https://cdnjs.cloudflare.com/ajax/libs/swagger-ui/5.11.0/swagger-ui.css" />
    <style>
        html { box-sizing: border-box; overflow: -y-scroll; }
        *, *:before, *:after { box-sizing: inherit; }
        body { margin: 0; background: #fafafa; }
    </style>
</head>
<body>
    <div id="swagger-ui"></div>
    <script src="https://cdnjs.cloudflare.com/ajax/libs/swagger-ui/5.11.0/swagger-ui-bundle.js"></script>
    <script src="https://cdnjs.cloudflare.com/ajax/libs/swagger-ui/5.11.0/swagger-ui-standalone-preset.js"></script>
    <script>
        window.onload = function() {
            const ui = SwaggerUIBundle({
                url: "/openapi.yaml",
                dom_id: '#swagger-ui',
                deepLinking: true,
                presets: [
                    SwaggerUIBundle.presets.apis,
                    SwaggerUIStandalonePreset
                ],
                plugins: [
                    SwaggerUIBundle.plugins.DownloadUrl
                ],
                layout: "StandaloneLayout"
            });
            window.ui = ui;
        };
    </script>
</body>
</html>`
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(swaggerUIHTML))
}
