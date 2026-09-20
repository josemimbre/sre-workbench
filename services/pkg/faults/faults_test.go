package faults

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	e := New(prometheus.NewRegistry())
	t.Cleanup(e.Close)
	return e
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func call(h http.Handler, path string) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code
}

func TestErrorFaultAlwaysFiresAtProbabilityOne(t *testing.T) {
	e := newTestEngine(t)
	if _, err := e.Add(Fault{Type: TypeError, Status: 503, Probability: 1, TTLSeconds: 30}); err != nil {
		t.Fatal(err)
	}
	h := e.Middleware(okHandler())

	for i := range 5 {
		if code := call(h, "/checkout"); code != 503 {
			t.Fatalf("request %d: expected the injected 503, got %d", i, code)
		}
	}
}

func TestFaultIsScopedToItsPath(t *testing.T) {
	e := newTestEngine(t)
	if _, err := e.Add(Fault{Type: TypeError, Path: "/checkout", TTLSeconds: 30}); err != nil {
		t.Fatal(err)
	}
	h := e.Middleware(okHandler())

	if code := call(h, "/checkout"); code != http.StatusInternalServerError {
		t.Fatalf("expected the scoped path to fail, got %d", code)
	}
	if code := call(h, "/catalog"); code != http.StatusOK {
		t.Fatalf("expected other paths to be untouched, got %d", code)
	}
}

// The TTL is the safety property of the whole workbench: a forgotten fault would quietly
// corrupt every later experiment.
func TestFaultExpires(t *testing.T) {
	e := newTestEngine(t)
	if _, err := e.Add(Fault{Type: TypeError, TTLSeconds: 1}); err != nil {
		t.Fatal(err)
	}
	h := e.Middleware(okHandler())

	if code := call(h, "/checkout"); code != http.StatusInternalServerError {
		t.Fatalf("expected the fault to be active, got %d", code)
	}

	time.Sleep(1200 * time.Millisecond)

	if code := call(h, "/checkout"); code != http.StatusOK {
		t.Fatalf("expected the fault to have expired, got %d", code)
	}
	if n := len(e.List()); n != 0 {
		t.Fatalf("expected the expired fault to be gone from the list, got %d", n)
	}
}

func TestLatencyFaultDelaysTheRequest(t *testing.T) {
	e := newTestEngine(t)
	if _, err := e.Add(Fault{Type: TypeLatency, DelayMS: 120, TTLSeconds: 30}); err != nil {
		t.Fatal(err)
	}
	h := e.Middleware(okHandler())

	start := time.Now()
	if code := call(h, "/checkout"); code != http.StatusOK {
		t.Fatalf("a latency fault must not change the status, got %d", code)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("expected the request to be delayed, took %v", elapsed)
	}
}

func TestLatencyIsAppliedBeforeTheError(t *testing.T) {
	e := newTestEngine(t)
	for _, f := range []Fault{
		{Type: TypeError, Status: 500, TTLSeconds: 30},
		{Type: TypeLatency, DelayMS: 120, TTLSeconds: 30},
	} {
		if _, err := e.Add(f); err != nil {
			t.Fatal(err)
		}
	}
	h := e.Middleware(okHandler())

	start := time.Now()
	code := call(h, "/checkout")
	elapsed := time.Since(start)

	if code != http.StatusInternalServerError {
		t.Fatalf("expected the error fault to win, got %d", code)
	}
	// A real timeout costs the user the wait *and* the error. Serving the error
	// instantly would make the failure look cheaper than it is.
	if elapsed < 100*time.Millisecond {
		t.Fatalf("expected the failing request to pay the latency too, took %v", elapsed)
	}
}

func TestValidation(t *testing.T) {
	e := newTestEngine(t)
	cases := map[string]Fault{
		"unknown type":          {Type: "meltdown", TTLSeconds: 10},
		"probability above one": {Type: TypeError, Probability: 2, TTLSeconds: 10},
		"ttl above the cap":     {Type: TypeError, TTLSeconds: MaxTTLSeconds + 1},
		"status out of range":   {Type: TypeError, Status: 200, TTLSeconds: 10},
		"latency without delay": {Type: TypeLatency, TTLSeconds: 10},
		"too many cores":        {Type: TypeCPUBurn, Cores: 99, TTLSeconds: 10},
	}
	for name, f := range cases {
		if _, err := e.Add(f); err == nil {
			t.Errorf("%s: expected the fault to be rejected", name)
		}
	}
}

func TestDefaults(t *testing.T) {
	e := newTestEngine(t)
	f, err := e.Add(Fault{Type: TypeError})
	if err != nil {
		t.Fatal(err)
	}
	if f.Probability != 1 {
		t.Errorf("expected probability to default to 1, got %v", f.Probability)
	}
	if f.Status != http.StatusInternalServerError {
		t.Errorf("expected status to default to 500, got %d", f.Status)
	}
	if f.TTLSeconds != defaultTTL {
		t.Errorf("expected ttl to default to %ds, got %d", defaultTTL, f.TTLSeconds)
	}
	if f.ExpiresAt.Before(f.CreatedAt) {
		t.Error("expected the fault to expire after it was created")
	}
}

func TestClearRemovesEverything(t *testing.T) {
	e := newTestEngine(t)
	for range 3 {
		if _, err := e.Add(Fault{Type: TypeError, TTLSeconds: 60}); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.Clear(); n != 3 {
		t.Fatalf("expected 3 faults removed, got %d", n)
	}
	if code := call(e.Middleware(okHandler()), "/checkout"); code != http.StatusOK {
		t.Fatalf("expected a clean service after clearing, got %d", code)
	}
}

func TestAdminAPI(t *testing.T) {
	e := newTestEngine(t)
	h := e.Handler()

	post := httptest.NewRecorder()
	h.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/admin/faults",
		strings.NewReader(`{"type":"error","status":503,"probability":0.5,"ttl_seconds":30}`)))
	if post.Code != http.StatusCreated {
		t.Fatalf("expected 201 on create, got %d: %s", post.Code, post.Body)
	}

	list := httptest.NewRecorder()
	h.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/admin/faults", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("expected 200 on list, got %d", list.Code)
	}
	if n := len(e.List()); n != 1 {
		t.Fatalf("expected the fault to be stored, got %d", n)
	}

	bad := httptest.NewRecorder()
	h.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, "/admin/faults",
		strings.NewReader(`{"type":"error","status":42}`)))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("expected an invalid fault to be rejected, got %d", bad.Code)
	}

	del := httptest.NewRecorder()
	h.ServeHTTP(del, httptest.NewRequest(http.MethodDelete, "/admin/faults", nil))
	if del.Code != http.StatusOK {
		t.Fatalf("expected 200 on clear, got %d", del.Code)
	}
	if n := len(e.List()); n != 0 {
		t.Fatalf("expected no faults left, got %d", n)
	}
}
