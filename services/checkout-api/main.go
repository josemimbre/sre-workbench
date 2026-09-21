// checkout-api is the entry point of the workbench: a write endpoint whose SLIs are
// availability and latency. It has no dependencies yet; what it does have is a fault
// engine, so the SLIs can be broken on demand and watched.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/josemimbre/sre-workbench/services/pkg/faults"
	"github.com/josemimbre/sre-workbench/services/pkg/metrics"
)

const serviceName = "checkout-api"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	version := env("VERSION", "v1")
	baseLatency := time.Duration(envInt("BASE_LATENCY_MS", 40)) * time.Millisecond

	m := metrics.New(serviceName, version)
	engine := faults.New(prometheus.DefaultRegisterer)
	api := &api{log: log, baseLatency: baseLatency}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /checkout", api.checkout)

	// Only user traffic goes through the middleware chain. /metrics, /healthz and the
	// fault admin API stay outside it: scraping and control-plane calls are not user
	// traffic, and the admin API has to keep working while the service is broken.
	//
	// The fault engine sits *inside* the metrics middleware so that injected latency
	// and injected errors land in the SLI exactly as a user would experience them.
	root := http.NewServeMux()
	root.Handle("/metrics", promhttp.Handler())
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	root.Handle("/admin/", engine.Handler())
	root.Handle("/", m.Middleware(mux, engine.Middleware(mux)))

	addr := ":" + env("PORT", "8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Info("listening", "service", serviceName, "version", version, "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown failed", "err", err)
	}
	log.Info("stopped")
}

type api struct {
	log         *slog.Logger
	baseLatency time.Duration
}

type checkoutRequest struct {
	ItemID string `json:"item_id"`
	Qty    int    `json:"qty"`
}

type checkoutResponse struct {
	OrderID string `json:"order_id"`
	ItemID  string `json:"item_id"`
	Qty     int    `json:"qty"`
}

func (a *api) checkout(w http.ResponseWriter, r *http.Request) {
	var req checkoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed body"})
		return
	}
	// A rejected order is a correct answer, not a failure: 4xx must never count as a
	// bad event in the availability SLI.
	if req.ItemID == "" || req.Qty <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "item_id and qty are required"})
		return
	}

	a.work()

	writeJSON(w, http.StatusOK, checkoutResponse{
		OrderID: strconv.FormatUint(rand.Uint64(), 36),
		ItemID:  req.ItemID,
		Qty:     req.Qty,
	})
}

// work simulates service time with a log-normal distribution, which is what real
// request latency looks like: a tight body and a long right tail.
func (a *api) work() {
	d := float64(a.baseLatency) * math.Exp(rand.NormFloat64()*0.45)
	time.Sleep(time.Duration(d))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}
