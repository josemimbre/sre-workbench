// Package faults is the deliberate-failure engine of the workbench. Every failure in
// this project is explicit, scoped and time-boxed: faults are added through an HTTP API,
// they fire with a probability, and they expire on their own. A fault that outlives its
// experiment would silently contaminate the next one, so the TTL is not optional.
//
// The engine is meant to sit *inside* the RED middleware: the SLI has to see the damage
// exactly as a user would.
package faults

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Type is the kind of damage a fault does.
type Type string

const (
	// TypeError returns a status code instead of serving the request.
	TypeError Type = "error"
	// TypeLatency delays the request before serving it normally.
	TypeLatency Type = "latency"
	// TypeCPUBurn spins CPU in the background, degrading every endpoint at once.
	TypeCPUBurn Type = "cpu_burn"
)

const (
	// MaxTTLSeconds bounds how long any fault can live. One hour is already longer
	// than every scenario in the plan.
	MaxTTLSeconds = 3600
	// cpuBurnMaxTTL is stricter: a runaway CPU fault degrades the host, not just the
	// experiment.
	cpuBurnMaxTTL = 600
	maxCPUCores   = 4
	defaultTTL    = 60
)

// Fault is one injected failure. The zero value is not usable; go through Engine.Add,
// which normalises and validates it.
type Fault struct {
	ID   string `json:"id"`
	Type Type   `json:"type"`
	// Path scopes the fault to one URL path. Empty means every path the engine wraps.
	Path string `json:"path,omitempty"`
	// Probability is the fraction of matching requests that are hit, 0..1.
	Probability float64 `json:"probability"`
	// Status is the code returned by an error fault.
	Status int `json:"status,omitempty"`
	// DelayMS and JitterMS shape a latency fault: delay ± jitter, uniformly.
	DelayMS  int `json:"delay_ms,omitempty"`
	JitterMS int `json:"jitter_ms,omitempty"`
	// Cores and Duty shape a CPU burn: how many goroutines spin, and what share of
	// the time they spend spinning rather than sleeping.
	Cores int     `json:"cores,omitempty"`
	Duty  float64 `json:"duty,omitempty"`

	TTLSeconds int       `json:"ttl_seconds"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	// Note records where the fault came from, e.g. the scenario that injected it.
	Note string `json:"note,omitempty"`

	stop func()
}

// Remaining is how long the fault still has to live, for the UI countdown.
func (f Fault) Remaining() time.Duration {
	d := time.Until(f.ExpiresAt)
	if d < 0 {
		return 0
	}
	return d
}

func (f *Fault) normalize() error {
	switch f.Type {
	case TypeError, TypeLatency, TypeCPUBurn:
	default:
		return fmt.Errorf("unknown fault type %q", f.Type)
	}

	if f.Probability == 0 {
		f.Probability = 1
	}
	if f.Probability < 0 || f.Probability > 1 {
		return fmt.Errorf("probability must be between 0 and 1, got %v", f.Probability)
	}

	if f.TTLSeconds == 0 {
		f.TTLSeconds = defaultTTL
	}
	if f.TTLSeconds < 1 {
		return fmt.Errorf("ttl_seconds must be at least 1")
	}
	maxTTL := MaxTTLSeconds
	if f.Type == TypeCPUBurn {
		maxTTL = cpuBurnMaxTTL
	}
	if f.TTLSeconds > maxTTL {
		return fmt.Errorf("ttl_seconds must be at most %d for a %s fault", maxTTL, f.Type)
	}

	switch f.Type {
	case TypeError:
		if f.Status == 0 {
			f.Status = http.StatusInternalServerError
		}
		if f.Status < 400 || f.Status > 599 {
			return fmt.Errorf("status must be between 400 and 599, got %d", f.Status)
		}
	case TypeLatency:
		if f.DelayMS <= 0 {
			return fmt.Errorf("delay_ms must be positive")
		}
		if f.JitterMS < 0 {
			return fmt.Errorf("jitter_ms cannot be negative")
		}
	case TypeCPUBurn:
		if f.Cores == 0 {
			f.Cores = 1
		}
		if f.Cores < 1 || f.Cores > maxCPUCores {
			return fmt.Errorf("cores must be between 1 and %d, got %d", maxCPUCores, f.Cores)
		}
		if f.Duty == 0 {
			f.Duty = 0.7
		}
		if f.Duty <= 0 || f.Duty > 1 {
			return fmt.Errorf("duty must be between 0 and 1, got %v", f.Duty)
		}
	}
	return nil
}

// Engine holds the active faults of one service.
type Engine struct {
	mu     sync.RWMutex
	faults map[string]*Fault

	injected *prometheus.CounterVec
	active   *prometheus.GaugeVec

	janitorStop func()
}

// New builds an engine and starts the janitor that expires faults.
func New(reg prometheus.Registerer) *Engine {
	f := promauto.With(reg)
	e := &Engine{
		faults: map[string]*Fault{},
		injected: f.NewCounterVec(prometheus.CounterOpts{
			Name: "faults_injected_total",
			Help: "Requests actually hit by an injected fault, by type.",
		}, []string{"type"}),
		active: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "faults_active",
			Help: "Currently active injected faults, by type.",
		}, []string{"type"}),
	}

	// Expiry is driven by a janitor rather than only by request traffic, because a
	// cpu_burn fault has to stop even when nothing is being served.
	done := make(chan struct{})
	e.janitorStop = sync.OnceFunc(func() { close(done) })
	go func() {
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				e.prune()
			}
		}
	}()
	return e
}

// Close stops the janitor and every running fault. Only tests need it.
func (e *Engine) Close() {
	e.janitorStop()
	e.Clear()
}

// Add validates, starts and stores a fault.
func (e *Engine) Add(in Fault) (Fault, error) {
	if err := in.normalize(); err != nil {
		return Fault{}, err
	}

	now := time.Now()
	in.ID = fmt.Sprintf("%s-%05d", in.Type, rand.IntN(100000))
	in.CreatedAt = now
	in.ExpiresAt = now.Add(time.Duration(in.TTLSeconds) * time.Second)

	f := &in
	if f.Type == TypeCPUBurn {
		stop := make(chan struct{})
		f.stop = sync.OnceFunc(func() { close(stop) })
		burnCPU(f.Cores, f.Duty, stop)
	}

	e.mu.Lock()
	e.faults[f.ID] = f
	e.mu.Unlock()
	e.syncGauges()

	return *f, nil
}

// Remove deletes one fault, stopping its side effects.
func (e *Engine) Remove(id string) bool {
	e.mu.Lock()
	f, ok := e.faults[id]
	if ok {
		delete(e.faults, id)
	}
	e.mu.Unlock()

	if ok && f.stop != nil {
		f.stop()
	}
	e.syncGauges()
	return ok
}

// Clear removes every fault and reports how many there were. This is the panic button
// of the whole workbench.
func (e *Engine) Clear() int {
	e.mu.Lock()
	old := e.faults
	e.faults = map[string]*Fault{}
	e.mu.Unlock()

	for _, f := range old {
		if f.stop != nil {
			f.stop()
		}
	}
	e.syncGauges()
	return len(old)
}

// List returns the active faults, soonest to expire first.
func (e *Engine) List() []Fault {
	e.prune()
	e.mu.RLock()
	defer e.mu.RUnlock()

	out := make([]Fault, 0, len(e.faults))
	for _, f := range e.faults {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ExpiresAt.Before(out[j].ExpiresAt) })
	return out
}

func (e *Engine) prune() {
	now := time.Now()
	var expired []*Fault

	e.mu.Lock()
	for id, f := range e.faults {
		if now.After(f.ExpiresAt) {
			expired = append(expired, f)
			delete(e.faults, id)
		}
	}
	e.mu.Unlock()

	for _, f := range expired {
		if f.stop != nil {
			f.stop()
		}
	}
	if len(expired) > 0 {
		e.syncGauges()
	}
}

func (e *Engine) syncGauges() {
	counts := map[Type]float64{TypeError: 0, TypeLatency: 0, TypeCPUBurn: 0}
	e.mu.RLock()
	for _, f := range e.faults {
		counts[f.Type]++
	}
	e.mu.RUnlock()

	for t, n := range counts {
		e.active.WithLabelValues(string(t)).Set(n)
	}
}

// Middleware applies the active faults to a request. It belongs inside the RED
// middleware: the metrics have to record the injected damage, not hide it.
func (e *Engine) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, f := range e.matching(r.URL.Path) {
			if rand.Float64() >= f.Probability {
				continue
			}
			switch f.Type {
			case TypeLatency:
				e.injected.WithLabelValues(string(f.Type)).Inc()
				time.Sleep(latencyOf(f))
			case TypeError:
				e.injected.WithLabelValues(string(f.Type)).Inc()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(f.Status)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": "injected fault",
					"fault": f.ID,
				})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// matching returns the request-scoped faults for a path, latency before error: a request
// that is going to fail should still pay for the delay, which is what a real timeout
// looks like.
func (e *Engine) matching(path string) []Fault {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var out []Fault
	for _, f := range e.faults {
		if f.Type == TypeCPUBurn {
			continue
		}
		if f.Path != "" && f.Path != path {
			continue
		}
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type == TypeLatency && out[j].Type != TypeLatency })
	return out
}

func latencyOf(f Fault) time.Duration {
	d := time.Duration(f.DelayMS) * time.Millisecond
	if f.JitterMS > 0 {
		j := time.Duration(rand.IntN(2*f.JitterMS+1)-f.JitterMS) * time.Millisecond
		d += j
	}
	if d < 0 {
		return 0
	}
	return d
}

// burnCPU spins `cores` goroutines that alternate between busy work and sleep, so the
// duty cycle controls how much of a core each one really takes.
func burnCPU(cores int, duty float64, stop <-chan struct{}) {
	const window = 20 * time.Millisecond
	busy := time.Duration(float64(window) * duty)
	idle := window - busy

	for i := 0; i < cores; i++ {
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				deadline := time.Now().Add(busy)
				for time.Now().Before(deadline) { //nolint:revive // the spinning is the point
				}
				if idle > 0 {
					time.Sleep(idle)
				}
			}
		}()
	}
}
