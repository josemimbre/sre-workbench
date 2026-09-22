// catalog-api is the workbench's dependency: a read endpoint backed by Postgres, with a
// small cache in front and a deliberately small connection pool behind.
//
// It exists to show what checkout-api cannot show on its own. checkout-api fails when you
// tell it to; this one fails the way real services fail — by running out of a shared
// resource. Squeeze the pool and nothing here is broken: queries still succeed, the code
// still works, and requests start queueing for a connection until the caller upstream
// gives up. That queueing is non-linear, which is exactly why an average latency tells
// you nothing useful about it.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/josemimbre/sre-workbench/services/pkg/faults"
	"github.com/josemimbre/sre-workbench/services/pkg/metrics"
)

const (
	serviceName = "catalog-api"
	// clientClosedRequest is nginx's 499: the caller gave up before we answered.
	clientClosedRequest = 499
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	version := env("VERSION", "v1")

	db, err := openDB(env("DATABASE_URL", "postgres://workbench:workbench@postgres:5432/workbench?sslmode=disable"),
		envInt("DB_MAX_CONNS", 8))
	if err != nil {
		log.Error("cannot reach the database", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	m := metrics.New(serviceName, version)
	engine := faults.New(prometheus.DefaultRegisterer)
	store := &catalog{
		db:       db,
		log:      log,
		cache:    map[string]cacheEntry{},
		cacheTTL: time.Duration(envInt("CACHE_TTL_MS", 2000)) * time.Millisecond,
		dbLatency: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "catalog_db_query_duration_seconds",
			Help:    "Time spent getting a connection and running one query.",
			Buckets: metrics.SLOBuckets,
		}),
		cacheHits: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "catalog_cache_lookups_total",
			Help: "Cache lookups by result.",
		}, []string{"result"}),
	}
	collectPoolStats(db)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /items/{id}", store.item)

	root := http.NewServeMux()
	root.Handle("/metrics", promhttp.Handler())
	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	root.Handle("/admin/", engine.Handler())
	root.Handle("/", m.Middleware(mux, engine.Middleware(mux)))

	addr := ":" + env("PORT", "8081")
	srv := &http.Server{Addr: addr, Handler: root, ReadHeaderTimeout: 5 * time.Second}

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
	_ = srv.Shutdown(shutdownCtx)
	log.Info("stopped")
}

func openDB(url string, maxConns int) (*sql.DB, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	// The pool is the point. database/sql queues callers when every connection is busy,
	// so this number is the knob that turns a healthy service into a saturated one.
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	db.SetConnMaxLifetime(30 * time.Minute)

	// Postgres takes a few seconds to accept connections on a cold start.
	deadline := time.Now().Add(30 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err = db.PingContext(ctx)
		cancel()
		if err == nil {
			return db, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// collectPoolStats exposes the queue in front of the pool. WaitCount climbing while
// everything still returns 200 is saturation caught before it becomes an outage.
func collectPoolStats(db *sql.DB) {
	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "catalog_db_connections_in_use",
		Help: "Connections currently checked out of the pool.",
	}, func() float64 { return float64(db.Stats().InUse) })

	promauto.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "catalog_db_connections_max",
		Help: "Maximum connections the pool will open.",
	}, func() float64 { return float64(db.Stats().MaxOpenConnections) })

	promauto.NewCounterFunc(prometheus.CounterOpts{
		Name: "catalog_db_waits_total",
		Help: "Requests that had to wait for a free connection.",
	}, func() float64 { return float64(db.Stats().WaitCount) })

	promauto.NewCounterFunc(prometheus.CounterOpts{
		Name: "catalog_db_wait_seconds_total",
		Help: "Total time spent waiting for a free connection.",
	}, func() float64 { return db.Stats().WaitDuration.Seconds() })
}

type cacheEntry struct {
	item    item
	expires time.Time
}

type item struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PriceCents int    `json:"price_cents"`
}

type catalog struct {
	db  *sql.DB
	log *slog.Logger

	mu       sync.RWMutex
	cache    map[string]cacheEntry
	cacheTTL time.Duration

	dbLatency prometheus.Histogram
	cacheHits *prometheus.CounterVec
}

func (c *catalog) item(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if found, ok := c.fromCache(id); ok {
		c.cacheHits.WithLabelValues("hit").Inc()
		writeJSON(w, http.StatusOK, found)
		return
	}
	c.cacheHits.WithLabelValues("miss").Inc()

	found, err := c.fromDB(r.Context(), id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// A 404 for something that genuinely does not exist is a correct answer, and
		// must not count against availability.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such item"})
		return
	case errors.Is(err, context.Canceled):
		// The caller hung up before we answered, so there is nobody left to fail. Left
		// as a 500 this would be a service spending its error budget on its callers'
		// impatience: put a slow fault in front of a client with a short timeout and the
		// dependency's SLI collapses for something it did not do. 499 is nginx's code
		// for it, and being a 4xx it stays out of the budget.
		w.WriteHeader(clientClosedRequest)
		return
	case err != nil:
		// Everything else — a timeout waiting for a connection included — is ours.
		c.log.Error("lookup failed", "id", id, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "catalog unavailable"})
		return
	}

	c.store(id, found)
	writeJSON(w, http.StatusOK, found)
}

func (c *catalog) fromCache(id string) (item, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.cache[id]
	if !ok || time.Now().After(entry.expires) {
		return item{}, false
	}
	return entry.item, true
}

func (c *catalog) store(id string, v item) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[id] = cacheEntry{item: v, expires: time.Now().Add(c.cacheTTL)}
}

func (c *catalog) fromDB(ctx context.Context, id string) (item, error) {
	start := time.Now()
	defer func() { c.dbLatency.Observe(time.Since(start).Seconds()) }()

	var found item
	err := c.db.QueryRowContext(ctx,
		`SELECT id, name, price_cents FROM items WHERE id = $1`, id,
	).Scan(&found.ID, &found.Name, &found.PriceCents)
	return found, err
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
