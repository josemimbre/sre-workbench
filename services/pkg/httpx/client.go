// Package httpx is the caller's half of a dependency. It exists mostly for the timeout:
// a client with no deadline turns a slow dependency into a slow caller, and then into a
// slow caller's caller, until something upstream runs out of patience or connections.
//
// It also records what happened from the caller's point of view, which is rarely the same
// story the dependency tells about itself. A request that timed out is a failure here and
// a success over there, and that gap is the whole reason to measure both ends.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ErrNotFound is returned for a 404, which is a definite answer rather than a failure:
// the dependency did its job and the thing is not there.
var ErrNotFound = errors.New("not found")

type Client struct {
	name string
	base string
	http *http.Client

	calls    *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func New(reg prometheus.Registerer, name, base string, timeout time.Duration) *Client {
	f := promauto.With(reg)
	return &Client{
		name: name,
		base: base,
		http: &http.Client{Timeout: timeout},
		calls: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dependency_requests_total",
			Help: "Calls to a dependency, by outcome as the caller saw it.",
		}, []string{"dependency", "outcome"}),
		duration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "dependency_request_duration_seconds",
			Help:    "Time a dependency call took, measured by the caller.",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.3, 0.5, 0.75, 1, 2, 5},
		}, []string{"dependency"}),
	}
}

// Get fetches a JSON document, decoding it into out.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	start := time.Now()
	outcome := "ok"
	defer func() {
		c.calls.WithLabelValues(c.name, outcome).Inc()
		c.duration.WithLabelValues(c.name).Observe(time.Since(start).Seconds())
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		outcome = "bad_request"
		return err
	}

	res, err := c.http.Do(req)
	if err != nil {
		// A timeout and a refused connection are different problems with the same
		// symptom upstream, so they get different labels.
		outcome = "unreachable"
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			outcome = "timeout"
		}
		return fmt.Errorf("%s: %w", c.name, err)
	}
	defer res.Body.Close()

	switch {
	case res.StatusCode == http.StatusNotFound:
		outcome = "not_found"
		_, _ = io.Copy(io.Discard, res.Body)
		return ErrNotFound
	case res.StatusCode >= 500:
		outcome = "server_error"
		_, _ = io.Copy(io.Discard, res.Body)
		return fmt.Errorf("%s returned %d", c.name, res.StatusCode)
	case res.StatusCode >= 400:
		outcome = "client_error"
		_, _ = io.Copy(io.Discard, res.Body)
		return fmt.Errorf("%s returned %d", c.name, res.StatusCode)
	}

	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		outcome = "unreadable"
		return fmt.Errorf("%s: %w", c.name, err)
	}
	return nil
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}
