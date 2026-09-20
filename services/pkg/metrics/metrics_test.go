package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func newTestHandler(t *testing.T) (http.Handler, *Metrics) {
	t.Helper()
	m := NewWith(prometheus.NewRegistry(), "test-service", "v0")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /checkout", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /boom", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	return m.Middleware(mux, mux), m
}

func do(t *testing.T, h http.Handler, method, path string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader("{}")))
	return rec.Code
}

func TestRouteLabelUsesThePattern(t *testing.T) {
	h, m := newTestHandler(t)
	do(t, h, http.MethodPost, "/checkout")

	if got := testutil.ToFloat64(m.requests.WithLabelValues("POST /checkout", "POST", "200")); got != 1 {
		t.Fatalf("expected 1 request labelled with the route pattern, got %v", got)
	}
}

// Unmatched paths must collapse into one label value; otherwise a scanner hitting random
// URLs would blow up the cardinality of every SLI query.
func TestUnmatchedPathsShareOneLabel(t *testing.T) {
	h, m := newTestHandler(t)
	do(t, h, http.MethodGet, "/does-not-exist")
	do(t, h, http.MethodGet, "/neither-does-this")

	if got := testutil.ToFloat64(m.requests.WithLabelValues("unmatched", "GET", "404")); got != 2 {
		t.Fatalf("expected both unknown paths under the same label, got %v", got)
	}
}

func TestStatusCodeIsCaptured(t *testing.T) {
	h, m := newTestHandler(t)
	if code := do(t, h, http.MethodGet, "/boom"); code != http.StatusInternalServerError {
		t.Fatalf("expected the middleware to pass the status through, got %d", code)
	}
	if got := testutil.ToFloat64(m.requests.WithLabelValues("GET /boom", "GET", "500")); got != 1 {
		t.Fatalf("expected the 500 to be counted, got %v", got)
	}
}

// The latency SLI is a ratio of buckets, so 300ms has to be an actual bucket boundary.
func TestSLOThresholdIsABucketBoundary(t *testing.T) {
	for _, want := range []float64{0.1, 0.3} {
		if !contains(SLOBuckets, want) {
			t.Errorf("bucket %v missing: the latency SLI cannot be computed without it", want)
		}
	}
}

func TestInFlightReturnsToZero(t *testing.T) {
	h, m := newTestHandler(t)
	do(t, h, http.MethodPost, "/checkout")

	if got := testutil.ToFloat64(m.inFlight); got != 0 {
		t.Fatalf("expected the in-flight gauge to settle at 0, got %v", got)
	}
}

func contains(s []float64, v float64) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// Regression: a fault injector short-circuits the request before the mux ever routes it.
// The route label must still be the real route, otherwise a per-route SLI goes blank at
// precisely the moment the service is broken.
func TestRouteLabelSurvivesAShortCircuitingMiddleware(t *testing.T) {
	m := NewWith(prometheus.NewRegistry(), "test-service", "v0")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /checkout", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Stands in for the fault engine: answers 500 without calling the mux at all.
	breaker := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	do(t, m.Middleware(mux, breaker), http.MethodPost, "/checkout")

	if got := testutil.ToFloat64(m.requests.WithLabelValues("POST /checkout", "POST", "500")); got != 1 {
		t.Fatalf("expected the injected failure to keep its route label, got %v", got)
	}
}
