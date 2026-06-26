package chaos

import (
	"log/slog"
	"math/rand"
	"sync"
)

type Controller struct {
	postgresOffline bool
	redisOffline    bool
	packetLossRate  float64
	crashedRelays   map[string]bool
	mu              sync.RWMutex
}

var globalChaos *Controller
var once sync.Once

func GetController() *Controller {
	once.Do(func() {
		globalChaos = &Controller{
			crashedRelays: make(map[string]bool),
		}
	})
	return globalChaos
}

func (c *Controller) SetPostgresOffline(offline bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.postgresOffline = offline
	slog.Warn("chaos injection: postgres state changed", "offline", offline)
}

func (c *Controller) IsPostgresOffline() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.postgresOffline
}

func (c *Controller) SetRedisOffline(offline bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.redisOffline = offline
	slog.Warn("chaos injection: redis state changed", "offline", offline)
}

func (c *Controller) IsRedisOffline() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.redisOffline
}

func (c *Controller) SetPacketLossRate(rate float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.packetLossRate = rate
	slog.Warn("chaos injection: packet loss rate changed", "rate", rate)
}

func (c *Controller) ShouldDropPacket() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.packetLossRate <= 0.0 {
		return false
	}
	return rand.Float64() < c.packetLossRate
}

func (c *Controller) CrashRelay(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.crashedRelays[id] = true
	slog.Warn("chaos injection: relay crashed", "relay_id", id)
}

func (c *Controller) RecoverRelay(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.crashedRelays[id] = false
	slog.Info("chaos injection: relay recovered", "relay_id", id)
}

func (c *Controller) IsRelayCrashed(id string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.crashedRelays[id]
}
