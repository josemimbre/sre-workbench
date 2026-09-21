// console is the control plane of the workbench: one page that explains what is being
// measured, shows the SLIs and error budgets live, and lets you break the services on
// purpose.
//
// It exists mostly to avoid a browser problem. A static page cannot read Prometheus and
// several services' admin APIs without running into CORS and mixed content, so the
// console proxies both from a single origin. Keeping the queries server-side has a nicer
// side effect: the UI can show the exact PromQL behind every number it displays.
package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed ui
var uiFiles embed.FS

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	services, order := parseServices(env("SERVICES", "checkout-api=http://checkout-api:8080"))
	srv := &server{
		log:      log,
		prom:     newPromClient(env("PROMETHEUS_URL", "http://prometheus:9090")),
		services: services,
		order:    order,
		catalog:  buildCatalog(),
		client:   &http.Client{Timeout: 10 * time.Second},
		grafana:  env("GRAFANA_URL", "http://localhost:3000"),
		promURL:  env("PROMETHEUS_PUBLIC_URL", "http://localhost:9090"),
		alertURL: env("ALERTMANAGER_URL", "http://localhost:9093"),
	}
	if len(srv.services) == 0 {
		log.Error("no services configured", "hint", "SERVICES=name=http://host:port,...")
		os.Exit(1)
	}

	ui, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		log.Error("cannot open the embedded ui", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(ui)))
	mux.HandleFunc("GET /api/catalog", srv.handleCatalog)
	mux.HandleFunc("GET /api/summary", srv.handleSummary)
	mux.HandleFunc("GET /api/series", srv.handleSeries)
	mux.HandleFunc("GET /api/annotations", srv.handleAnnotations)
	mux.HandleFunc("GET /api/alerts", srv.handleAlerts)
	// Alertmanager posts here; it is not part of the browser-facing API.
	mux.HandleFunc("POST /api/alerts", srv.handleAlertWebhook)
	mux.HandleFunc("GET /api/faults", srv.handleListFaults)
	mux.HandleFunc("POST /api/faults", srv.handleAddFault)
	mux.HandleFunc("DELETE /api/faults", srv.handleClearFaults)
	mux.HandleFunc("DELETE /api/faults/{id}", srv.handleDeleteFault)
	mux.HandleFunc("POST /api/scenarios/{id}/run", srv.handleRunScenario)
	mux.HandleFunc("POST /api/crash", srv.handleCrash)

	addr := ":" + env("PORT", "8090")
	httpSrv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		log.Info("console listening", "addr", addr, "services", srv.serviceNames())
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	log.Info("stopped")
}

type server struct {
	log      *slog.Logger
	prom     *promClient
	services map[string]string
	// order keeps the configured services in the order they were declared, so the
	// default target is stable rather than whatever the map iterates first.
	order    []string
	catalog  catalog
	client   *http.Client
	grafana  string
	promURL  string
	alertURL string

	notifications notificationStore
}

func (s *server) serviceNames() []string { return s.order }

func (s *server) handleCatalog(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"catalog":  s.catalog,
		"services": s.serviceNames(),
		"links": map[string]string{
			"grafana":      s.grafana,
			"prometheus":   s.promURL,
			"alertmanager": s.alertURL,
		},
	})
}

// handleSummary evaluates every signal and budget in one round trip, in parallel. The UI
// polls this a few times a second's worth of data, so latency matters more than purity.
func (s *server) handleSummary(w http.ResponseWriter, r *http.Request) {
	job := s.job(r)

	all := append(append([]signalDef{}, s.catalog.Signals...), s.catalog.Budgets...)
	values := make(map[string]*float64, len(all))
	errs := make(map[string]string)

	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, sig := range all {
		wg.Add(1)
		go func(sig signalDef) {
			defer wg.Done()
			v, err := s.prom.instant(r.Context(), withJob(sig.Query, job))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[sig.Key] = err.Error()
				return
			}
			values[sig.Key] = v
		}(sig)
	}
	wg.Wait()

	writeJSON(w, http.StatusOK, map[string]any{
		"job":    job,
		"at":     time.Now(),
		"values": values,
		"errors": errs,
	})
}

func (s *server) handleSeries(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	sig, ok := s.lookupSignal(key)
	if !ok || sig.Series == "" {
		writeJSON(w, http.StatusNotFound, apiError{"unknown signal " + key})
		return
	}

	minutes := intParam(r, "minutes", 30, 1, 360)
	// Roughly 120 points per line: enough to see the shape, few enough to stay cheap.
	step := max(minutes*60/120, 5)

	points, err := s.prom.rangeQuery(r.Context(), withJob(sig.Series, s.job(r)), minutes, step)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiError{err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key, "points": points})
}

// handleAnnotations returns the stretches of time when a fault was active, so the charts
// can shade them. It reads the service's own faults_active gauge rather than trusting the
// browser to remember what it injected: a fault injected from curl, from a scenario, or
// before the page was opened has to show up on the chart just the same.
func (s *server) handleAnnotations(w http.ResponseWriter, r *http.Request) {
	minutes := intParam(r, "minutes", 30, 1, 360)
	step := max(minutes*60/120, 5)

	points, err := s.prom.rangeQuery(r.Context(),
		withJob(`sum(faults_active{job="$JOB"})`, s.job(r)), minutes, step)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiError{err.Error()})
		return
	}

	type span struct {
		Start   float64 `json:"start"`
		End     float64 `json:"end"`
		Ongoing bool    `json:"ongoing"`
	}

	spans := []span{}
	var open *span
	var lastTS float64
	for _, p := range points {
		if p[0] == nil {
			continue
		}
		ts, v := *p[0], 0.0
		if p[1] != nil {
			v = *p[1]
		}
		lastTS = ts

		switch {
		case v > 0 && open == nil:
			// A gauge sampled every `step` seconds only tells us the fault was already
			// active by now, so the span starts one step earlier than the first sample.
			open = &span{Start: ts - float64(step)}
		case v == 0 && open != nil:
			open.End = ts
			spans = append(spans, *open)
			open = nil
		}
	}
	if open != nil {
		open.End = lastTS
		open.Ongoing = true
		spans = append(spans, *open)
	}

	writeJSON(w, http.StatusOK, map[string]any{"spans": spans})
}

func (s *server) lookupSignal(key string) (signalDef, bool) {
	for _, sig := range append(append([]signalDef{}, s.catalog.Signals...), s.catalog.Budgets...) {
		if sig.Key == key {
			return sig, true
		}
	}
	return signalDef{}, false
}

func (s *server) handleListFaults(w http.ResponseWriter, r *http.Request) {
	s.proxy(w, r, http.MethodGet, "/admin/faults", nil)
}

func (s *server) handleAddFault(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{"unreadable body"})
		return
	}
	s.proxy(w, r, http.MethodPost, "/admin/faults", body)
}

func (s *server) handleClearFaults(w http.ResponseWriter, r *http.Request) {
	s.proxy(w, r, http.MethodDelete, "/admin/faults", nil)
}

func (s *server) handleDeleteFault(w http.ResponseWriter, r *http.Request) {
	s.proxy(w, r, http.MethodDelete, "/admin/faults/"+r.PathValue("id"), nil)
}

func (s *server) handleCrash(w http.ResponseWriter, r *http.Request) {
	s.proxy(w, r, http.MethodPost, "/admin/crash", nil)
}

// handleRunScenario injects a scenario's whole fault set at once. There is no server-side
// timer: each fault carries its own TTL, so a scenario ends even if the console dies.
func (s *server) handleRunScenario(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var sc *scenario
	for i := range s.catalog.Scenarios {
		if s.catalog.Scenarios[i].ID == id {
			sc = &s.catalog.Scenarios[i]
			break
		}
	}
	if sc == nil {
		writeJSON(w, http.StatusNotFound, apiError{"unknown scenario " + id})
		return
	}

	base, err := s.serviceURL(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{err.Error()})
		return
	}

	created := make([]json.RawMessage, 0, len(sc.Faults))
	for _, f := range sc.Faults {
		body, _ := json.Marshal(f)
		res, err := s.forward(r.Context(), http.MethodPost, base+"/admin/faults", body)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, apiError{err.Error()})
			return
		}
		if res.status >= 300 {
			writeJSON(w, res.status, json.RawMessage(res.body))
			return
		}
		created = append(created, res.body)
	}

	s.log.Info("scenario started", "scenario", sc.ID, "faults", len(created))
	writeJSON(w, http.StatusCreated, map[string]any{
		"scenario": sc.ID,
		"faults":   created,
		"ends_at":  time.Now().Add(time.Duration(sc.Duration) * time.Second),
	})
}

// proxy forwards a control-plane call to one service's admin API.
func (s *server) proxy(w http.ResponseWriter, r *http.Request, method, path string, body []byte) {
	base, err := s.serviceURL(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{err.Error()})
		return
	}

	res, err := s.forward(r.Context(), method, base+path, body)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiError{err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.status)
	_, _ = w.Write(res.body)
}

type proxyResult struct {
	status int
	body   []byte
}

func (s *server) forward(ctx context.Context, method, url string, body []byte) (proxyResult, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return proxyResult{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := s.client.Do(req)
	if err != nil {
		return proxyResult{}, fmt.Errorf("service unreachable: %w", err)
	}
	defer res.Body.Close()

	out, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return proxyResult{}, err
	}
	if len(out) == 0 {
		out = []byte("{}")
	}
	return proxyResult{status: res.StatusCode, body: out}, nil
}

// serviceURL resolves the ?service= parameter against the configured allow-list. Only
// known services can be targeted: the console must not become an open HTTP relay.
func (s *server) serviceURL(r *http.Request) (string, error) {
	name := r.URL.Query().Get("service")
	if name == "" {
		name = s.serviceNames()[0]
	}
	base, ok := s.services[name]
	if !ok {
		return "", fmt.Errorf("unknown service %q", name)
	}
	return base, nil
}

func (s *server) job(r *http.Request) string {
	if job := r.URL.Query().Get("job"); job != "" {
		if _, ok := s.services[job]; ok {
			return job
		}
	}
	return s.serviceNames()[0]
}

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// parseServices reads "name=url,name=url" into a map plus the declaration order.
func parseServices(raw string) (map[string]string, []string) {
	out := map[string]string{}
	var order []string
	for _, entry := range strings.Split(raw, ",") {
		name, url, ok := strings.Cut(strings.TrimSpace(entry), "=")
		if !ok || name == "" || url == "" {
			continue
		}
		if _, dup := out[name]; !dup {
			order = append(order, name)
		}
		out[name] = strings.TrimRight(url, "/")
	}
	return out, order
}

func intParam(r *http.Request, name string, fallback, minValue, maxValue int) int {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return fallback
	}
	return min(max(v, minValue), maxValue)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
