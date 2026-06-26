package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"log/slog"
)

// TokenBucket implements a simple thread-safe token-bucket rate limiter.
type TokenBucket struct {
	rate         float64 // Tokens refilled per second
	capacity     float64 // Maximum tokens
	tokens       float64
	lastRefilled time.Time
	mu           sync.Mutex
}

func NewTokenBucket(rate float64, capacity float64) *TokenBucket {
	return &TokenBucket{
		rate:         rate,
		capacity:     capacity,
		tokens:       capacity,
		lastRefilled: time.Now(),
	}
}

func (tb *TokenBucket) Allow() bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefilled).Seconds()
	tb.lastRefilled = now

	tb.tokens = tb.tokens + elapsed*tb.rate
	if tb.tokens > tb.capacity {
		tb.tokens = tb.capacity
	}

	if tb.tokens >= 1.0 {
		tb.tokens -= 1.0
		return true
	}
	return false
}

// IPRateLimiter tracks token buckets for individual client IP addresses.
type IPRateLimiter struct {
	ips      sync.Map
	rate     float64
	capacity float64
}

func NewIPRateLimiter(rate float64, capacity float64) *IPRateLimiter {
	return &IPRateLimiter{
		rate:     rate,
		capacity: capacity,
	}
}

func (lim *IPRateLimiter) Allow(ip string) bool {
	bucket, ok := lim.ips.Load(ip)
	if !ok {
		bucket = NewTokenBucket(lim.rate, lim.capacity)
		actual, loaded := lim.ips.LoadOrStore(ip, bucket)
		if loaded {
			bucket = actual
		}
	}
	return bucket.(*TokenBucket).Allow()
}

// TraceAndLoggerMiddleware extracts/injects a trace ID and records HTTP latency.
func TraceAndLoggerMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		traceID := c.GetHeader("X-Trace-ID")
		if traceID == "" {
			traceID = uuid.New().String()
		}
		c.Set("trace_id", traceID)
		c.Header("X-Trace-ID", traceID)

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()

		slog.Info("http_request_completed",
			"trace_id", traceID,
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", status,
			"duration_ms", latency.Milliseconds(),
			"client_ip", c.ClientIP(),
		)
	}
}

// RateLimitMiddleware applies category-based rate limits.
func RateLimitMiddleware(anonymousLim *IPRateLimiter, authenticatedLim *IPRateLimiter, internalLim *IPRateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		clientIP := c.ClientIP()
		authHeader := c.GetHeader("Authorization")

		var limiter *IPRateLimiter
		category := "anonymous"

		if authHeader == "internal-secret" {
			limiter = internalLim
			category = "internal"
		} else if authHeader != "" {
			limiter = authenticatedLim
			category = "authenticated"
		} else {
			limiter = anonymousLim
			category = "anonymous"
		}

		if !limiter.Allow(clientIP) {
			RespondWithError(c, http.StatusTooManyRequests, "RATE_LIMIT_EXCEEDED", "Too many requests. Please try again later.")
			return
		}

		c.Set("rate_limit_category", category)
		c.Next()
	}
}
