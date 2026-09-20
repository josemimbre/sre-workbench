package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// promClient is a deliberately tiny Prometheus client: the console only ever needs a
// scalar and a line, and pulling in the official client would bring a dependency tree
// far larger than this whole service.
type promClient struct {
	base string
	http *http.Client
}

func newPromClient(base string) *promClient {
	return &promClient{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 10 * time.Second}}
}

type promResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Value  []any   `json:"value"`
			Values [][]any `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// point is one sample, as [unix seconds, value]. Nil values are holes in the line.
type point [2]*float64

// instant evaluates a query now. A missing result is nil rather than zero: "no data" and
// "zero" mean very different things when you are reading an SLI.
func (c *promClient) instant(ctx context.Context, query string) (*float64, error) {
	body, err := c.get(ctx, "/api/v1/query", url.Values{"query": {query}})
	if err != nil {
		return nil, err
	}
	if len(body.Data.Result) == 0 || len(body.Data.Result[0].Value) < 2 {
		return nil, nil
	}
	return parseSample(body.Data.Result[0].Value[1]), nil
}

// rangeQuery evaluates a query over a window, for the sparklines.
func (c *promClient) rangeQuery(ctx context.Context, query string, minutes, stepSeconds int) ([]point, error) {
	end := time.Now()
	start := end.Add(-time.Duration(minutes) * time.Minute)

	body, err := c.get(ctx, "/api/v1/query_range", url.Values{
		"query": {query},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
		"step":  {strconv.Itoa(stepSeconds)},
	})
	if err != nil {
		return nil, err
	}
	if len(body.Data.Result) == 0 {
		return []point{}, nil
	}

	raw := body.Data.Result[0].Values
	out := make([]point, 0, len(raw))
	for _, v := range raw {
		if len(v) < 2 {
			continue
		}
		ts := parseSample(v[0])
		out = append(out, point{ts, parseSample(v[1])})
	}
	return out, nil
}

func (c *promClient) get(ctx context.Context, path string, q url.Values) (*promResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prometheus unreachable: %w", err)
	}
	defer res.Body.Close()

	var body promResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("prometheus returned an unreadable response: %w", err)
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("query failed: %s", body.Error)
	}
	return &body, nil
}

// parseSample turns Prometheus' string-encoded floats into numbers. NaN and Inf become
// nil: JSON cannot carry them, and a chart should show a gap instead of a fake zero.
func parseSample(v any) *float64 {
	s, ok := v.(string)
	if !ok {
		if f, ok := v.(float64); ok && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return &f
		}
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return &f
}

func withWindow(query, window string) string {
	return strings.ReplaceAll(query, "$W", window)
}

func withJob(query, job string) string {
	return strings.ReplaceAll(query, "$JOB", job)
}

func ratio(v float64) string {
	return strconv.FormatFloat(v, 'f', 4, 64)
}
