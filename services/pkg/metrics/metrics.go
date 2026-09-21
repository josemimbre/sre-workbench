// Package metrics provides the RED instrumentation (rate, errors, duration) shared by
// every service in the workbench. The label set is chosen so the SLI queries work
// directly: the service identity comes from Prometheus' own `job` label, so the
// application never emits a conflicting one.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// SLOBuckets deliberately contains the 300ms threshold of the checkout latency SLO.
// The latency SLI is a ratio of buckets, so the threshold has to exist as a bucket
// boundary or the number cannot be computed without interpolating.
var SLOBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.3, 0.5, 0.75, 1, 2, 5}

// Metrics holds the RED instruments of a single service.
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inFlight prometheus.Gauge
}

// New registers the instruments on the default registry and records build info.
func New(service, version string) *Metrics {
	return NewWith(prometheus.DefaultRegisterer, service, version)
}

// NewWith registers on an explicit registry, which is what makes the package testable:
// the default registry is global and panics on a second registration.
func NewWith(reg prometheus.Registerer, service, version string) *Metrics {
	f := promauto.With(reg)

	f.NewGaugeVec(prometheus.GaugeOpts{
		Name: "sre_build_info",
		Help: "Build information of the running service, always 1.",
	}, []string{"service", "version"}).WithLabelValues(service, version).Set(1)

	return &Metrics{
		requests: f.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total HTTP requests by route, method and response code.",
		}, []string{"route", "method", "code"}),
		duration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request duration in seconds.",
			Buckets: SLOBuckets,
		}, []string{"route", "method"}),
		inFlight: f.NewGauge(prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "HTTP requests currently being served.",
		}),
	}
}

// Middleware wraps a handler with RED instrumentation.
//
// The route label is resolved from `routes` up front rather than read back from
// Request.Pattern afterwards. That matters as soon as anything sits between this
// middleware and the mux: a fault injector that short-circuits a request means the mux
// never routes it, and the label would collapse to the catch-all pattern exactly during
// an outage — losing the route dimension of the SLI when it is most needed.
//
// `next` is the rest of the chain, which normally ends at the same mux.
func (m *Metrics) Middleware(routes *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Unmatched paths collapse into a single label value: the alternative is
		// unbounded cardinality from random URLs.
		_, route := routes.Handler(r)
		if route == "" {
			route = "unmatched"
		}

		start := time.Now()
		m.inFlight.Inc()
		defer m.inFlight.Dec()

		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		m.requests.WithLabelValues(route, r.Method, strconv.Itoa(rec.status)).Inc()
		m.duration.WithLabelValues(route, r.Method).Observe(time.Since(start).Seconds())
	})
}

// recorder captures the status code written by the handler.
type recorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *recorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	r.wroteHeader = true
	return r.ResponseWriter.Write(b)
}
