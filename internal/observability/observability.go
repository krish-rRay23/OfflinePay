package observability

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.opentelemetry.io/otel/trace"
)

var (
	Tracer trace.Tracer

	// Prometheus Metrics
	PaymentsCreatedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "payments_created_total",
		Help: "Total number of payment intents created",
	})

	SettlementsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "settlements_total",
		Help: "Total number of settlements processed",
	}, []string{"status"})

	ReplayRejectionsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "replay_rejections_total",
		Help: "Total number of transactions rejected due to replay attacks",
	})

	TokenConsumptionTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "token_consumption_total",
		Help: "Total number of offline tokens consumed",
	})

	RelaySuccessTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "relay_success_total",
		Help: "Total number of successful relays forwarded",
	})

	RelayFailureTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "relay_failure_total",
		Help: "Total number of failed relay attempts",
	})

	ReconciliationRepairsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "reconciliation_repairs_total",
		Help: "Total number of reconciliation repairs executed",
	})

	SettlementLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "settlement_latency_ms",
		Help:    "Latency of the settlement process in milliseconds",
		Buckets: prometheus.DefBuckets,
	})

	ProjectionLag = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "projection_lag",
		Help: "Lag between the latest event and the processed projection checkpoint",
	})

	// New Production Readiness Metrics
	DLQSize = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "dlq_size",
		Help: "Total number of events currently in the Dead Letter Queue (DLQ)",
	})

	OutboxQueueSize = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "outbox_queue_size",
		Help: "Total number of pending events currently in the transactional outbox",
	})

	LedgerValidationStatus = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ledger_validation_status",
		Help: "Status of financial double-entry invariants (1 = balanced, 0 = discrepancy)",
	})

	RiskDecisionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "risk_decisions_total",
		Help: "Total number of decisions made by the risk engine",
	}, []string{"action"})

	ActiveWorkers = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "active_workers",
		Help: "Number of active background worker daemons running",
	}, []string{"worker"})
)

func InitMetrics() {
	prometheus.MustRegister(PaymentsCreatedTotal)
	prometheus.MustRegister(SettlementsTotal)
	prometheus.MustRegister(ReplayRejectionsTotal)
	prometheus.MustRegister(TokenConsumptionTotal)
	prometheus.MustRegister(RelaySuccessTotal)
	prometheus.MustRegister(RelayFailureTotal)
	prometheus.MustRegister(ReconciliationRepairsTotal)
	prometheus.MustRegister(SettlementLatency)
	prometheus.MustRegister(ProjectionLag)
	prometheus.MustRegister(DLQSize)
	prometheus.MustRegister(OutboxQueueSize)
	prometheus.MustRegister(LedgerValidationStatus)
	prometheus.MustRegister(RiskDecisionsTotal)
	prometheus.MustRegister(ActiveWorkers)
}

func InitTracer(ctx context.Context, serviceName string) (*sdktrace.TracerProvider, error) {
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint("localhost:4317"),
	)
	if err != nil {
		slog.Warn("failed to connect to OpenTelemetry collector, using fallback tracer provider", "error", err)
		tp := sdktrace.NewTracerProvider()
		otel.SetTracerProvider(tp)
		Tracer = tp.Tracer(serviceName)
		return tp, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	Tracer = tp.Tracer(serviceName)

	slog.Info("OpenTelemetry tracer successfully initialized", "service", serviceName)
	return tp, nil
}

func StartMetricsServer(port string) {
	http.Handle("/metrics", promhttp.Handler())
	slog.Info("starting metrics HTTP server", "port", port)
	go func() {
		if err := http.ListenAndServe(":"+port, nil); err != nil {
			slog.Error("metrics server failed", "error", err)
		}
	}()
}
