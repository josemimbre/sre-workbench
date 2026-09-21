package main

import "github.com/josemimbre/sre-workbench/services/pkg/faults"

// The catalog is the teaching material of the workbench, kept in one place and served to
// the UI: what each number means, what each fault does, and what each scenario should
// prove. The SLI queries live here too, so the console shows the user the exact PromQL
// behind every figure it displays.

// Playground SLO targets. The window is compressed to an hour so an experiment gives
// feedback in minutes rather than weeks.
const (
	availabilityTarget = 0.99 // 1% error budget
	latencyTarget      = 0.95 // 5% of requests may exceed the threshold
	latencyThreshold   = "0.3"
	budgetWindow       = "1h"
)

type signalDef struct {
	Key    string  `json:"key"`
	Name   string  `json:"name"`
	Spec   string  `json:"spec"`
	Why    string  `json:"why"`
	Query  string  `json:"query"`
	Series string  `json:"series,omitempty"`
	Unit   string  `json:"unit"`
	Target float64 `json:"target,omitempty"`
	Invert bool    `json:"invert,omitempty"`
	// Derivation explains what a recording rule stands for, so a precomputed name never
	// has to be taken on faith.
	Derivation string `json:"derivation,omitempty"`
}

type field struct {
	Name    string  `json:"name"`
	Label   string  `json:"label"`
	Kind    string  `json:"kind"`
	Min     float64 `json:"min"`
	Max     float64 `json:"max"`
	Step    float64 `json:"step"`
	Default float64 `json:"default"`
	Help    string  `json:"help"`
}

type faultDef struct {
	Type    string  `json:"type"`
	Name    string  `json:"name"`
	What    string  `json:"what"`
	Teaches string  `json:"teaches"`
	Fields  []field `json:"fields"`
}

type scenario struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Hypothesis string         `json:"hypothesis"`
	Watch      string         `json:"watch"`
	Duration   int            `json:"duration_seconds"`
	Faults     []faults.Fault `json:"faults"`
}

type topic struct {
	Title string   `json:"title"`
	Body  []string `json:"body"`
}

type catalog struct {
	Overview  overview    `json:"overview"`
	Signals   []signalDef `json:"signals"`
	Budgets   []signalDef `json:"budgets"`
	Faults    []faultDef  `json:"faults"`
	Scenarios []scenario  `json:"scenarios"`
	Topics    []topic     `json:"topics"`
	SLO       sloInfo     `json:"slo"`
}

type sloInfo struct {
	AvailabilityTarget float64 `json:"availability_target"`
	LatencyTarget      float64 `json:"latency_target"`
	LatencyThreshold   string  `json:"latency_threshold"`
	Window             string  `json:"window"`
}

// errorRatio is the bad-event ratio over a window. `or vector(0)` is what keeps the
// panel showing 0 instead of "no data" when nothing is failing.
const errorRatio = `(sum(rate(http_requests_total{job="$JOB", code=~"5.."}[$W])) or vector(0))
/
sum(rate(http_requests_total{job="$JOB"}[$W]))`

const latencyGoodRatio = `sum(rate(http_request_duration_seconds_bucket{job="$JOB", le="` + latencyThreshold + `"}[$W]))
/
sum(rate(http_request_duration_seconds_count{job="$JOB"}[$W]))`

func buildCatalog() catalog {
	return catalog{
		Overview: buildOverview(),
		SLO: sloInfo{
			AvailabilityTarget: availabilityTarget,
			LatencyTarget:      latencyTarget,
			LatencyThreshold:   latencyThreshold,
			Window:             budgetWindow,
		},
		Signals: []signalDef{
			{
				Key:  "availability",
				Name: "Availability SLI",
				Spec: "The proportion of requests that do not return 5xx, over the last 5 minutes.",
				Why: "This is the SLI the error budget is computed from. A 4xx is a correct answer " +
					"to a bad request, so it never counts as a bad event: the load generator sends 2% " +
					"invalid payloads precisely to keep that visible.",
				Query:  "1 - (" + withWindow(errorRatio, "5m") + ")",
				Series: "1 - (" + withWindow(errorRatio, "5m") + ")",
				Unit:   "ratio",
				Target: availabilityTarget,
			},
			{
				Key:  "latency",
				Name: "Latency SLI",
				Spec: "The proportion of requests served in under " + latencyThreshold + "s, over the last 5 minutes.",
				Why: "A ratio of histogram buckets, not a quantile. Quantiles cannot be averaged across " +
					"instances and cannot be turned into an error budget; a bucket ratio can do both.",
				Query:  withWindow(latencyGoodRatio, "5m"),
				Series: withWindow(latencyGoodRatio, "5m"),
				Unit:   "ratio",
				Target: latencyTarget,
			},
			{
				Key:    "throughput",
				Name:   "Throughput",
				Spec:   "Requests per second currently being served.",
				Why:    "The R in RED. It also tells you how much statistical weight the SLI has: a ratio over three requests means nothing.",
				Query:  `sum(rate(http_requests_total{job="$JOB"}[1m]))`,
				Series: `sum(rate(http_requests_total{job="$JOB"}[1m]))`,
				Unit:   "rps",
			},
			{
				Key:  "p99",
				Name: "p99 latency",
				Spec: "The 99th percentile of request duration, interpolated from the histogram.",
				Why: "Diagnostics only — the SLO is deliberately not defined on this number. Watch it " +
					"explode during a tail-latency fault while the latency SLI barely moves.",
				Query:  `histogram_quantile(0.99, sum by (le) (rate(http_request_duration_seconds_bucket{job="$JOB"}[1m])))`,
				Series: `histogram_quantile(0.99, sum by (le) (rate(http_request_duration_seconds_bucket{job="$JOB"}[1m])))`,
				Unit:   "seconds",
			},
			{
				Key:    "inflight",
				Name:   "In flight",
				Spec:   "Requests being served right now.",
				Why:    "Saturation shows up here before it shows up in latency, and long before it shows up in errors.",
				Query:  `sum(http_requests_in_flight{job="$JOB"})`,
				Series: `sum(http_requests_in_flight{job="$JOB"})`,
				Unit:   "count",
			},
			{
				Key:    "client_throughput",
				Name:   "Client-side throughput (k6)",
				Spec:   "Requests per second as counted by the load generator.",
				Why:    "When the client and the server disagree about what happened, the client is right. A crashed process is invisible to its own /metrics.",
				Query:  `sum(rate(k6_http_reqs_total[1m]))`,
				Series: `sum(rate(k6_http_reqs_total[1m]))`,
				Unit:   "rps",
			},
		},
		// These read Sloth's recording rules rather than recomputing the maths inline.
		// That is what a real setup does: the SLO spec is the source of truth, Prometheus
		// precomputes it once, and every consumer reads the same number.
		Budgets: []signalDef{
			{
				Key:  "availability_budget",
				Name: "Availability budget left",
				Spec: "How much of the 1% error budget is still unspent in the last hour.",
				Why: "The budget is the whole point: it turns 'is the service healthy?' into a number you " +
					"can spend. At 0% left, you stop shipping features and start fixing reliability.",
				Query:      `slo:period_error_budget_remaining:ratio{sloth_service="$JOB", sloth_slo="availability"}`,
				Series:     `slo:period_error_budget_remaining:ratio{sloth_service="$JOB", sloth_slo="availability"}`,
				Derivation: "1 - (slo:sli_error:ratio_rate1h / slo:error_budget:ratio), where the error budget is vector(1-0.99).",
				Unit:       "ratio",
			},
			{
				Key:  "availability_burn",
				Name: "Availability burn rate",
				Spec: "How many times faster than sustainable the budget is being spent right now.",
				Why: "A burn rate of 1 exhausts the budget exactly at the end of the window. 14.4 pages, " +
					"1 opens a ticket — the thresholds the alerts are built on.",
				Query:      `slo:current_burn_rate:ratio{sloth_service="$JOB", sloth_slo="availability"}`,
				Series:     `slo:current_burn_rate:ratio{sloth_service="$JOB", sloth_slo="availability"}`,
				Derivation: "slo:sli_error:ratio_rate1m / slo:error_budget:ratio — the shortest window, so it reacts fast.",
				Unit:       "rate",
				Invert:     true,
			},
			{
				Key:        "latency_budget",
				Name:       "Latency budget left",
				Spec:       "How much of the 5% slow-request budget is still unspent in the last hour.",
				Why:        "Latency has its own budget. A service can be 100% available and still be out of budget.",
				Query:      `slo:period_error_budget_remaining:ratio{sloth_service="$JOB", sloth_slo="latency"}`,
				Series:     `slo:period_error_budget_remaining:ratio{sloth_service="$JOB", sloth_slo="latency"}`,
				Derivation: "Same shape as availability, with bad events counted as requests slower than 300ms.",
				Unit:       "ratio",
			},
			{
				Key:        "latency_burn",
				Name:       "Latency burn rate",
				Spec:       "How fast the slow-request budget is being spent right now.",
				Why:        "Run the tail-latency scenario and watch this climb while availability never moves.",
				Query:      `slo:current_burn_rate:ratio{sloth_service="$JOB", sloth_slo="latency"}`,
				Series:     `slo:current_burn_rate:ratio{sloth_service="$JOB", sloth_slo="latency"}`,
				Derivation: "slo:sli_error:ratio_rate1m / slo:error_budget:ratio for the latency SLO.",
				Unit:       "rate",
				Invert:     true,
			},
		},
		Faults: []faultDef{
			{
				Type: "error",
				Name: "Errors",
				What: "Returns the chosen status code instead of serving the request, to a fraction of the traffic.",
				Teaches: "The most direct way to spend budget. Try 30% for two minutes and watch exactly " +
					"one hour of budget disappear.",
				Fields: []field{
					{Name: "probability", Label: "Share of requests", Kind: "ratio", Min: 0.01, Max: 1, Step: 0.01, Default: 0.3, Help: "1 means every request fails."},
					{Name: "status", Label: "Status code", Kind: "int", Min: 400, Max: 599, Step: 1, Default: 500, Help: "Only 5xx counts as a bad event. Try 429 to see a fault that does not touch the SLI."},
					{Name: "ttl_seconds", Label: "Duration", Kind: "seconds", Min: 5, Max: 3600, Step: 5, Default: 120, Help: "The fault removes itself when this runs out."},
				},
			},
			{
				Type: "latency",
				Name: "Latency",
				What: "Delays a fraction of requests before serving them normally.",
				Teaches: "Availability stays at 100% while the latency SLO collapses. Hit 5% of requests " +
					"with 2s to see the p99 explode while the SLI hardly moves.",
				Fields: []field{
					{Name: "probability", Label: "Share of requests", Kind: "ratio", Min: 0.01, Max: 1, Step: 0.01, Default: 1, Help: "Small shares hurt the tail, not the median."},
					{Name: "delay_ms", Label: "Added delay", Kind: "ms", Min: 10, Max: 10000, Step: 10, Default: 300, Help: "The SLO threshold is 300ms."},
					{Name: "jitter_ms", Label: "Jitter", Kind: "ms", Min: 0, Max: 2000, Step: 10, Default: 0, Help: "Spreads the delay uniformly by ± this amount."},
					{Name: "ttl_seconds", Label: "Duration", Kind: "seconds", Min: 5, Max: 3600, Step: 5, Default: 300, Help: ""},
				},
			},
		},
		Scenarios: []scenario{
			{
				ID:   "short-blip",
				Name: "Short blip",
				Hypothesis: "30% of requests fail for 2 minutes. Over a 1-hour window that is 0.30 × (2/60) = 1% " +
					"of all requests — exactly the whole error budget. A two-minute incident can cost an entire hour.",
				Watch:    "Availability budget left should fall to roughly 0%. The 5m burn rate peaks around 30x.",
				Duration: 120,
				Faults: []faults.Fault{
					{Type: faults.TypeError, Status: 500, Probability: 0.3, TTLSeconds: 120, Note: "short-blip"},
				},
			},
			{
				ID:   "total-outage",
				Name: "Total outage",
				Hypothesis: "Everything fails for 60 seconds: 1/60 = 1.67% of the hour's requests, so about 167% " +
					"of the budget. One minute of hard downtime is already an overdraft.",
				Watch:    "Budget goes negative. Client-side throughput keeps going while every request 503s.",
				Duration: 60,
				Faults: []faults.Fault{
					{Type: faults.TypeError, Status: 503, Probability: 1, TTLSeconds: 60, Note: "total-outage"},
				},
			},
			{
				ID:   "slow-degradation",
				Name: "Slow degradation",
				Hypothesis: "Every request gets 250ms slower for 10 minutes. Availability never moves; the latency " +
					"SLI collapses because the base latency plus 250ms sits past the 300ms threshold.",
				Watch:    "Two budgets diverging: availability untouched, latency budget draining fast.",
				Duration: 600,
				Faults: []faults.Fault{
					{Type: faults.TypeLatency, DelayMS: 250, Probability: 1, TTLSeconds: 600, Note: "slow-degradation"},
				},
			},
			{
				ID:   "tail-latency",
				Name: "Tail latency",
				Hypothesis: "Only 5% of requests are hit, but with 2 extra seconds. The p99 explodes while the " +
					"latency SLI loses about 5 points — the classic argument for SLIs over percentiles.",
				Watch:    "p99 jumps to ~2s, latency SLI drops to ~95%, the SLO is only just breached.",
				Duration: 300,
				Faults: []faults.Fault{
					{Type: faults.TypeLatency, DelayMS: 2000, Probability: 0.05, TTLSeconds: 300, Note: "tail-latency"},
				},
			},
			{
				ID:   "flaky",
				Name: "Slow burn",
				Hypothesis: "A steady 2% failure rate for 30 minutes also spends 100% of the hourly budget, but " +
					"no single moment looks alarming. This is what the long-window alerts exist to catch.",
				Watch:    "The 5m burn rate hovers around 2x — never dramatic, yet the budget drains to zero.",
				Duration: 1800,
				Faults: []faults.Fault{
					{Type: faults.TypeError, Status: 500, Probability: 0.02, TTLSeconds: 1800, Note: "flaky"},
				},
			},
			{
				ID:   "latency-and-errors",
				Name: "Timeout cascade",
				Hypothesis: "Requests are delayed by 800ms and 20% of them also fail. Failing requests still pay " +
					"the delay first, which is what a real upstream timeout feels like.",
				Watch:    "Both budgets drain at once, and the failed requests are slow, not instant.",
				Duration: 180,
				Faults: []faults.Fault{
					{Type: faults.TypeLatency, DelayMS: 800, Probability: 1, TTLSeconds: 180, Note: "latency-and-errors"},
					{Type: faults.TypeError, Status: 504, Probability: 0.2, TTLSeconds: 180, Note: "latency-and-errors"},
				},
			},
		},
		Topics: []topic{
			{
				Title: "SLI, SLO, error budget",
				Body: []string{
					"An SLI is a measurement: the proportion of requests that were good. An SLO is a target for that measurement over a window. The error budget is what is left over: 100% minus the target.",
					"At a 99% target, 1% of requests are allowed to fail. That 1% is not waste, it is the budget you spend on shipping changes, and it makes 'can we deploy on Friday?' a question with a numeric answer.",
					"The windows here are 1 hour instead of the usual 28 days, so an experiment gives feedback in minutes instead of weeks.",
				},
			},
			{
				Title: "Burn rate",
				Body: []string{
					"Burn rate is how many times faster than sustainable you are spending the budget. A burn rate of 1 uses exactly the whole budget by the end of the window; a burn rate of 14.4 uses 2% of a 30-day budget in an hour.",
					"That is why alerting on burn rate beats alerting on a raw error rate: it takes both severity and duration into account, so a two-minute blip and a two-day drizzle can both page you when they deserve it.",
					"Phase 3 of the plan turns this into the four multi-window alerts from the SRE Workbook.",
				},
			},
			{
				Title: "Why a bucket ratio and not a percentile",
				Body: []string{
					"The latency SLI counts requests under 300ms and divides by the total. It never computes a percentile.",
					"Percentiles cannot be averaged or added across instances, and there is no meaningful way to turn a p99 of 412ms into an error budget. A ratio of good events is just a number of successes, so it aggregates and it budgets.",
					"The p99 card is still here, because it is excellent for diagnosis. Run the tail-latency scenario and watch the two disagree.",
				},
			},
			{
				Title: "What counts as a bad event",
				Body: []string{
					"Only 5xx responses count against availability. A 400 means the user sent nonsense and the service correctly said so — counting it would make your own validation look like an outage.",
					"Try the error fault with status 429 instead of 500: the fault clearly hurts users, and the SLI does not move. Deciding what counts is a design decision, not a technicality.",
					"The same argument applies to a 404 for something that genuinely does not exist, versus a 503 caused by a saturated connection pool.",
				},
			},
			{
				Title: "Server-side versus client-side",
				Body: []string{
					"The service measures itself, and k6 measures the service. Both feed the same Prometheus.",
					"They disagree exactly when it matters: a process that crashes cannot report its own failures, and a request that never arrives is invisible to the server. The client's number is the one closer to the truth.",
					"Crash the process and compare the two throughput lines.",
				},
			},
			{
				Title: "Why the alerts are generated, not written",
				Body: []string{
					"The SLO is declared once, in observability/slo, as a target and a pair of queries for good and bad events. Sloth turns that into seven recording rules per SLO plus four multi-window alerts, and `make slo-gen` commits the result.",
					"Hand-writing them is where SLO setups rot: the dashboard ends up computing the ratio slightly differently from the alert, and nobody notices until an incident. One declaration, one generator, one set of numbers.",
					"The generated file is committed, so the workbench comes up without anyone having to run the generator first. Read observability/prometheus/rules/checkout-api.yml to see exactly what was produced.",
				},
			},
			{
				Title: "Compressed windows have a floor",
				Body: []string{
					"The SRE Workbook's alert catalogue — page at 14.4x over an hour, ticket at 1x over three days — is calibrated for a 30-day SLO period. Scaled proportionally to this playground's one-hour period, its long windows would land at about five seconds, below the scrape interval, so they could never be measured at all.",
					"So the burn rate thresholds are kept and the budget percentages give way. Paging at 14.4x over a two-minute window means the page arrives once 48% of the hour's budget is gone, where the Workbook's version arrives at 2%.",
					"That is the real trade of a compressed period: feedback in minutes instead of days, paid for in budget efficiency. The arithmetic is written out in observability/slo/windows/1h.yaml.",
				},
			},
			{
				Title: "Reading the budget honestly",
				Body: []string{
					"Every scenario's arithmetic assumes the one-hour window is full of steady traffic. A rolling window does not wait for that: right after the stack comes up, or after a service is rebuilt and its counters start again, the window holds twenty minutes of data and still calls itself an hour.",
					"The ratio it reports is then an average over what it has, not over an hour, so the same incident reads as a larger share of the budget than it really is. Give the workbench an hour of quiet before trusting a budget number to the decimal.",
					"The burn rate does not have this problem — it is computed over a one-minute window, which fills up immediately. That is another reason alerting on burn rate beats alerting on budget remaining.",
				},
			},
			{
				Title: "Why every fault has a TTL",
				Body: []string{
					"A fault with no expiry is a fault you forget about, and it silently poisons every later experiment.",
					"Every injection here carries a deadline, capped at one hour, and clear all is one click away.",
					"This is also why faults are injected rather than coded in: the service has no hidden failure modes, only the ones you asked for.",
				},
			},
		},
	}
}
