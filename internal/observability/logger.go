package observability

import (
	"context"
	"log/slog"
)

type contextKey string

const (
	TraceIDKey  contextKey = "trace_id"
	TxnIDKey    contextKey = "txn_id"
	DeviceIDKey contextKey = "device_id"
	TokenIDKey  contextKey = "token_id"
	RelayIDKey  contextKey = "relay_id"
)

// Correlation ID context builders

func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, TraceIDKey, id)
}

func WithTxnID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, TxnIDKey, id)
}

func WithDeviceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, DeviceIDKey, id)
}

func WithTokenID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, TokenIDKey, id)
}

func WithRelayID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, RelayIDKey, id)
}

// ContextHandler wraps a slog.Handler to inject context values into log records.
type ContextHandler struct {
	slog.Handler
}

func NewContextHandler(h slog.Handler) *ContextHandler {
	return &ContextHandler{Handler: h}
}

func (ch *ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if ctx == nil {
		return ch.Handler.Handle(ctx, r)
	}

	keys := []contextKey{TraceIDKey, TxnIDKey, DeviceIDKey, TokenIDKey, RelayIDKey}
	for _, k := range keys {
		if val, ok := ctx.Value(k).(string); ok && val != "" {
			r.AddAttrs(slog.String(string(k), val))
		}
	}

	return ch.Handler.Handle(ctx, r)
}

func (ch *ContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ContextHandler{Handler: ch.Handler.WithAttrs(attrs)}
}

func (ch *ContextHandler) WithGroup(name string) slog.Handler {
	return &ContextHandler{Handler: ch.Handler.WithGroup(name)}
}
