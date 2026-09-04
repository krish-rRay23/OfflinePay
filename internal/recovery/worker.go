package recovery

import (
	"context"
	"log/slog"
	"time"
)

type Worker struct {
	manager  *Manager
	interval time.Duration
}

func NewWorker(manager *Manager, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = time.Second
	}
	return &Worker{manager: manager, interval: interval}
}
func (w *Worker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.manager.RunOnce(ctx, 50); err != nil {
				slog.Error("recovery scheduler iteration failed", "error", err)
			}
		}
	}
}
