package eventbus

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"offlinepay/internal/chaos"

	"github.com/redis/go-redis/v9"
)

type EventBus struct {
	client *redis.Client
}

func NewEventBus(redisAddr string, redisPassword string) (*EventBus, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: redisPassword,
		DB:       0,
	})

	// Ping Redis to verify connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	slog.Info("connected to Redis successfully")
	return &EventBus{client: client}, nil
}

func (eb *EventBus) Close() error {
	return eb.client.Close()
}

func (eb *EventBus) GetClient() *redis.Client {
	return eb.client
}

// Publish an event to a Redis Stream
func (eb *EventBus) Publish(ctx context.Context, stream string, eventType string, payload string, eventID string) error {
	if chaos.GetController().IsRedisOffline() {
		return fmt.Errorf("redis connection lost (simulated chaos)")
	}
	args := &redis.XAddArgs{
		Stream: stream,
		Values: map[string]interface{}{
			"event_id":  eventID,
			"type":      eventType,
			"payload":   payload,
			"timestamp": time.Now().Format(time.RFC3339),
		},
	}

	err := eb.client.XAdd(ctx, args).Err()
	if err != nil {
		return fmt.Errorf("failed to publish to stream %s: %w", stream, err)
	}

	slog.Debug("event published to Redis Stream", "stream", stream, "type", eventType)
	return nil
}

// Subscribe to a Redis Stream using Consumer Groups
func (eb *EventBus) Subscribe(ctx context.Context, stream string, group string, consumer string, handler func(eventID string, eventType string, payload string) error) {
	// Create consumer group if not exists
	err := eb.client.XGroupCreateMkStream(ctx, stream, group, "0").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		slog.Error("failed to create consumer group", "stream", stream, "group", group, "error", err)
		return
	}

	slog.Info("subscribed to stream", "stream", stream, "group", group, "consumer", consumer)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				// Read new messages (">" denotes messages that have not been delivered to other consumers)
				streams, err := eb.client.XReadGroup(ctx, &redis.XReadGroupArgs{
					Group:    group,
					Consumer: consumer,
					Streams:  []string{stream, ">"},
					Count:    10,
					Block:    1 * time.Second,
				}).Result()

				if err != nil {
					if err != redis.Nil {
						slog.Error("error reading from stream group", "stream", stream, "group", group, "error", err)
					}
					time.Sleep(1 * time.Second)
					continue
				}

				for _, streamData := range streams {
					for _, message := range streamData.Messages {
						eventID, _ := message.Values["event_id"].(string)
						evType, ok1 := message.Values["type"].(string)
						payload, ok2 := message.Values["payload"].(string)

						if ok1 && ok2 {
							if err := handler(eventID, evType, payload); err != nil {
								slog.Error("failed to handle stream message", "message_id", message.ID, "error", err)
							}
						}

						// Acknowledge processing
						err = eb.client.XAck(ctx, stream, group, message.ID).Err()
						if err != nil {
							slog.Error("failed to ACK stream message", "message_id", message.ID, "error", err)
						}
					}
				}
			}
		}
	}()
}
