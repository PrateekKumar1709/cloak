// Package metrics exposes Prometheus counters/histograms for Cloak.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "cloak_requests_total",
		Help: "Total proxied requests",
	}, []string{"route", "status"})

	RedactionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "cloak_redactions_total",
		Help: "Total redactions by category",
	}, []string{"category"})

	BlockedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "cloak_blocked_total",
		Help: "Requests blocked by policy",
	})

	DetectionSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "cloak_detection_seconds",
		Help:    "Detection pipeline latency",
		Buckets: []float64{0.001, 0.01, 0.05, 0.1, 0.2, 0.4, 0.8, 1.5, 3},
	})
)

// Handler returns the Prometheus HTTP handler.
func Handler() http.Handler {
	return promhttp.Handler()
}
