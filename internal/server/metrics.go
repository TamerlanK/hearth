package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/pprof"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	connectionsCurrent = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hearth_connections_current",
		Help: "Client connections currently open.",
	})
	connectionsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hearth_connections_total",
		Help: "Client connections admitted since start.",
	})
	messagesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "hearth_messages_total",
		Help: "Events produced by the hub, counted once each rather than once per recipient.",
	}, []string{"kind"})
	droppedMessagesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hearth_dropped_messages_total",
		Help: "Events dropped because a client's outbox was full.",
	})
	rateLimitedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "hearth_rate_limited_total",
		Help: "Lines rejected by a per-client rate limiter.",
	})
	roomsCurrent = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "hearth_rooms_current",
		Help: "Rooms that currently exist.",
	})
	fanoutSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "hearth_message_fanout_seconds",
		Help:    "Time for the hub to hand one message to every outbox in the room.",
		Buckets: prometheus.ExponentialBuckets(1e-6, 4, 10),
	})
)

func MetricsHandler(log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if _, err := io.WriteString(w, "ok\n"); err != nil {
			log.Debug("write healthz response", "event", "healthz", "err", err)
		}
	})
	return mux
}
