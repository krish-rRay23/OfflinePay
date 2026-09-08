package eventbus

import (
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestShouldAcknowledgeMessage(t *testing.T) {
	tests := []struct {
		name        string
		message     redis.XMessage
		handlerErr  error
		wantAck     bool
		wantEventID string
		wantType    string
		wantPayload string
	}{
		{
			name: "acknowledges message on successful handling",
			message: redis.XMessage{
				ID: "1-0",
				Values: map[string]interface{}{
					"event_id": "evt-1",
					"type":     "payment.settled",
					"payload":  `{"txn_id":"tx-1"}`,
				},
			},
			wantAck:     true,
			wantEventID: "evt-1",
			wantType:    "payment.settled",
			wantPayload: `{"txn_id":"tx-1"}`,
		},
		{
			name: "does not acknowledge when handler fails",
			message: redis.XMessage{
				ID: "2-0",
				Values: map[string]interface{}{
					"event_id": "evt-2",
					"type":     "payment.failed",
					"payload":  `{"txn_id":"tx-2"}`,
				},
			},
			handlerErr: errors.New("temporary downstream failure"),
			wantAck:    false,
		},
		{
			name: "does not acknowledge when required fields are missing",
			message: redis.XMessage{
				ID: "3-0",
				Values: map[string]interface{}{
					"event_id": "evt-3",
					"type":     "payment.failed",
				},
			},
			wantAck: false,
		},
		{
			name: "accepts byte values from redis response decoding",
			message: redis.XMessage{
				ID: "4-0",
				Values: map[string]interface{}{
					"event_id": []byte("evt-4"),
					"type":     []byte("payment.settled"),
					"payload":  []byte(`{"txn_id":"tx-4"}`),
				},
			},
			wantAck:     true,
			wantEventID: "evt-4",
			wantType:    "payment.settled",
			wantPayload: `{"txn_id":"tx-4"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotEventID, gotType, gotPayload string

			handler := func(eventID string, eventType string, payload string) error {
				gotEventID, gotType, gotPayload = eventID, eventType, payload
				return tt.handlerErr
			}

			gotAck := shouldAcknowledgeMessage(tt.message, handler)
			if gotAck != tt.wantAck {
				t.Fatalf("shouldAcknowledgeMessage() = %v, want %v", gotAck, tt.wantAck)
			}

			if tt.wantAck {
				if gotEventID != tt.wantEventID || gotType != tt.wantType || gotPayload != tt.wantPayload {
					t.Fatalf("handler args = (%q, %q, %q), want (%q, %q, %q)",
						gotEventID, gotType, gotPayload, tt.wantEventID, tt.wantType, tt.wantPayload)
				}
			}
		})
	}
}
