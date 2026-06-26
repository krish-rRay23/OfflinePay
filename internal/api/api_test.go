package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"offlinepay/internal/config"
)

func TestHealthEndpoints(t *testing.T) {
	cfg := &config.Config{}
	cfg.Security.RateLimits.Anonymous = 1000
	cfg.Security.RateLimits.Authenticated = 10000
	cfg.Security.RateLimits.Internal = 100000

	server := NewServer(cfg, nil, nil, nil, nil, nil, nil, nil)
	r := server.Handler()

	t.Run("Liveness check returns 200 UP", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/live", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), `"status":"UP"`) {
			t.Errorf("unexpected body: %s", w.Body.String())
		}
	})

	t.Run("Health check returns 200 UP", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}
	})
}

func TestStandardErrorPayload(t *testing.T) {
	cfg := &config.Config{}
	cfg.Security.RateLimits.Anonymous = 1000
	cfg.Security.RateLimits.Authenticated = 10000
	cfg.Security.RateLimits.Internal = 100000

	server := NewServer(cfg, nil, nil, nil, nil, nil, nil, nil)
	r := server.Handler()

	t.Run("Querying missing v1 route returns standard APIError JSON", func(t *testing.T) {
		req, _ := http.NewRequest("POST", "/api/v1/identity/devices", strings.NewReader("invalid-json"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status 400, got %d", w.Code)
		}

		var apiErr APIError
		err := json.Unmarshal(w.Body.Bytes(), &apiErr)
		if err != nil {
			t.Fatalf("failed to parse standard error JSON: %v", err)
		}

		if apiErr.Code != "INVALID_REQUEST" {
			t.Errorf("expected code INVALID_REQUEST, got %s", apiErr.Code)
		}
		if apiErr.TraceID == "" {
			t.Errorf("expected generated trace ID, got empty string")
		}
		if apiErr.Timestamp.IsZero() {
			t.Errorf("expected timestamp to be non-zero")
		}
	})
}

func TestRateLimiterMiddleware(t *testing.T) {
	cfg := &config.Config{}
	// Extremely low limit to trigger rate limiter
	cfg.Security.RateLimits.Anonymous = 1
	cfg.Security.RateLimits.Authenticated = 1000
	cfg.Security.RateLimits.Internal = 1000

	server := NewServer(cfg, nil, nil, nil, nil, nil, nil, nil)
	r := server.Handler()

	// Make multiple quick requests from the same IP to trigger rate limits
	t.Run("Rapid anonymous requests trigger 429 Too Many Requests", func(t *testing.T) {
		req, _ := http.NewRequest("POST", "/api/v1/identity/devices", strings.NewReader("invalid-json"))
		req.Header.Set("Content-Type", "application/json")

		// First request should pass (well, trigger bad request validation, not rate limit)
		w1 := httptest.NewRecorder()
		r.ServeHTTP(w1, req)
		if w1.Code != http.StatusBadRequest {
			t.Errorf("expected status 400 for validation, got %d", w1.Code)
		}

		// Immediate second request from same IP should get rate-limited
		time.Sleep(5 * time.Millisecond)
		w2 := httptest.NewRecorder()
		r.ServeHTTP(w2, req)

		if w2.Code != http.StatusTooManyRequests {
			t.Errorf("expected status 429, got %d. Body: %s", w2.Code, w2.Body.String())
		}

		var apiErr APIError
		_ = json.Unmarshal(w2.Body.Bytes(), &apiErr)
		if apiErr.Code != "RATE_LIMIT_EXCEEDED" {
			t.Errorf("expected code RATE_LIMIT_EXCEEDED, got %s", apiErr.Code)
		}
	})
}
