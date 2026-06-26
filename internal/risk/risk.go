package risk

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"

	"offlinepay/internal/domain"
	"offlinepay/internal/observability"

	"github.com/redis/go-redis/v9"
)

// Risk Action outputs
const (
	ActionAllow        = "ALLOW"
	ActionThrottle     = "THROTTLE"
	ActionManualReview = "MANUAL_REVIEW"
	ActionReject       = "REJECT"
)

type RiskEngine struct {
	redisClient *redis.Client
}

type RiskAssessment struct {
	Action string  `json:"action"` // ALLOW, THROTTLE, MANUAL_REVIEW, REJECT
	Score  float64 `json:"score"`  // 0.0 to 1.0
	Reason string  `json:"reason"`
}

func NewRiskEngine(rdb *redis.Client) *RiskEngine {
	return &RiskEngine{redisClient: rdb}
}

// SetRedisClient (useful for testing/simulation fallback)
func (re *RiskEngine) SetRedisClient(rdb *redis.Client) {
	re.redisClient = rdb
}

func (re *RiskEngine) Assess(
	ctx context.Context,
	dev *domain.Device,
	intent *domain.PaymentIntentPayload,
	hopCount int,
	recentFailures int,
	latitude float64,
	longitude float64,
) RiskAssessment {
	score := 0.0
	reason := "low risk"

	// 1. Device identity checks
	if dev == nil {
		return RiskAssessment{
			Action: ActionReject,
			Score:  1.0,
			Reason: "unregistered device",
		}
	}

	if dev.Status == domain.DeviceRevoked || dev.Status == domain.DeviceCompromised {
		return RiskAssessment{
			Action: ActionReject,
			Score:  1.0,
			Reason: fmt.Sprintf("device status is %s", dev.Status),
		}
	}

	// Device trust score impact
	score += (1.0 - dev.TrustScore) * 0.3

	// 2. Transaction Amount Spike check (unusual payment size, e.g. > $500 / 50000 cents is REJECT/REVIEW for offline)
	if intent.Amount > 50000 {
		score += 0.4
		reason = "transaction amount spike"
	} else if intent.Amount > 15000 {
		score += 0.15
		reason = "moderate transaction amount"
	}

	// 3. Proximity Relay Anomalies
	if hopCount > 5 {
		score += 0.25
		reason = "excessive relay hop count"
	}

	// 4. Device Reputation & Historical Failures
	if recentFailures > 5 {
		score += 0.4
		reason = "reputation alert: excessive historical failures"
	} else if recentFailures > 2 {
		score += 0.15
		reason = "reputation warning: moderate historical failures"
	}

	// 5. Sliding-Window Velocity Checks (using Redis)
	if re.redisClient != nil {
		now := time.Now().UnixNano()
		windowStart := time.Now().Add(-1 * time.Minute).UnixNano()

		// Key per device/sender
		velocityKey := fmt.Sprintf("velocity:%s", dev.DeviceID)

		// Remove elements older than window (1 minute)
		_, err := re.redisClient.ZRemRangeByScore(ctx, velocityKey, "0", fmt.Sprintf("%d", windowStart)).Result()
		if err == nil {
			// Add current transaction timestamp
			_, err = re.redisClient.ZAdd(ctx, velocityKey, redis.Z{Score: float64(now), Member: now}).Result()
			if err == nil {
				// Set TTL so we don't leak memory in Redis
				_ = re.redisClient.Expire(ctx, velocityKey, 5*time.Minute)

				// Count transactions in the last minute
				count, err := re.redisClient.ZCard(ctx, velocityKey).Result()
				if err == nil {
					if count > 10 {
						score += 0.5
						reason = "velocity check: excessive transactions per minute"
					} else if count > 4 {
						score += 0.2
						reason = "velocity check: elevated transaction frequency"
					}
				}
			}
		}
	}

	// 6. Location Drift Simulation
	// In a real system, compare current coordinates with the last transaction location stored in Redis
	if re.redisClient != nil && latitude != 0 && longitude != 0 {
		locKey := fmt.Sprintf("last_loc:%s", dev.DeviceID)
		lastLocStr, err := re.redisClient.Get(ctx, locKey).Result()
		if err == nil {
			var lastLoc struct {
				Lat  float64 `json:"lat"`
				Long float64 `json:"long"`
				Time int64   `json:"time"`
			}
			if errUnmarshal := json.Unmarshal([]byte(lastLocStr), &lastLoc); errUnmarshal == nil {
				// Calculate distance (haversine formula or simple Euclidean distance for simulation)
				dx := (latitude - lastLoc.Lat) * 111.0 // approx km per degree lat
				dy := (longitude - lastLoc.Long) * 111.0
				distance := math.Sqrt(dx*dx + dy*dy)

				timeDiffHours := float64(time.Now().Unix()-lastLoc.Time) / 3600.0
				if timeDiffHours > 0 {
					speed := distance / timeDiffHours // km/h
					if speed > 800.0 && distance > 100.0 { // Faster than commercial aircraft -> drift flag
						score += 0.35
						reason = "location drift: physically impossible travel velocity"
					}
				}
			}
		}

		// Store current location
		currentLocBytes, _ := json.Marshal(map[string]interface{}{
			"lat":  latitude,
			"long": longitude,
			"time": time.Now().Unix(),
		})
		_ = re.redisClient.Set(ctx, locKey, currentLocBytes, 24*time.Hour).Err()
	}

	// Determine final action based on score
	var action string
	if score >= 0.8 {
		action = ActionReject
	} else if score >= 0.5 {
		action = ActionManualReview
	} else if score >= 0.3 {
		action = ActionThrottle
	} else {
		action = ActionAllow
	}

	slog.Info("risk engine v3 completed assessment",
		"device_id", dev.DeviceID,
		"score", score,
		"action", action,
		"reason", reason,
	)

	observability.RiskDecisionsTotal.WithLabelValues(action).Inc()

	return RiskAssessment{
		Action: action,
		Score:  score,
		Reason: reason,
	}
}
