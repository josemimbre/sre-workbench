package main

// The overview is the map of the workbench: what is running, how a single request turns
// into a number on a dashboard, and where in that chain each fault is injected. It is
// written for somebody who has never seen an SLI before, because the point of the
// project is to teach the idea, not to assume it.

type component struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Kind   string   `json:"kind"` // client | service | infra
	Image  string   `json:"image"`
	Port   string   `json:"port,omitempty"`
	Role   string   `json:"role"`
	Detail []string `json:"detail"`
	Emits  []string `json:"emits,omitempty"`
}

type edge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label"`
}

type journeyStep struct {
	N      int    `json:"n"`
	Where  string `json:"where"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

type fact struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Note  string `json:"note"`
}

type term struct {
	Term  string `json:"term"`
	Plain string `json:"plain"`
	Here  string `json:"here"`
}

type injectionPoint struct {
	Fault       string   `json:"fault"`
	Where       string   `json:"where"`
	Effect      string   `json:"effect"`
	VisibleIn   []string `json:"visible_in"`
	InvisibleIn []string `json:"invisible_in"`
}

type overview struct {
	Title           string           `json:"title"`
	Lede            []string         `json:"lede"`
	Components      []component      `json:"components"`
	Edges           []edge           `json:"edges"`
	Journey         []journeyStep    `json:"journey"`
	Baseline        []fact           `json:"baseline"`
	Vocabulary      []term           `json:"vocabulary"`
	InjectionPoints []injectionPoint `json:"injection_points"`
}

func buildOverview() overview {
	return overview{
		Title: "What is running here",
		Lede: []string{
			"This is a laboratory, not a product. It runs one small pretend service — a shop checkout — " +
				"sends it a steady stream of pretend customers, and measures how reliable it is. Then it lets " +
				"you break it on purpose and watch what that does to the measurement.",
			"The interesting part is never the service. It is the gap between what is actually happening to " +
				"users and what your monitoring says is happening. Most of the scenarios below exist to open " +
				"that gap and let you look into it.",
			"Five containers are running. Every number on this page comes from one of them, and every one of " +
				"them can be reached from your browser, so nothing here is a black box.",
		},

		Components: []component{
			{
				ID:    "loadgen",
				Name:  "loadgen",
				Kind:  "client",
				Image: "grafana/k6",
				Role:  "The pretend customers.",
				Detail: []string{
					"A load generator that sends about 20 checkout requests every second, continuously, forever. " +
						"Without it the service would sit idle and there would be nothing to measure: a reliability " +
						"ratio computed over three requests is noise, not a signal.",
					"Roughly 2% of what it sends is deliberately invalid — an empty item, a quantity of zero. Those " +
						"come back as 400 errors, and they are supposed to. Keeping them in the traffic is a constant " +
						"reminder that not every error is a failure.",
					"It also measures the service from the outside and pushes its own numbers into Prometheus. That " +
						"gives you a second opinion: when the service's self-reported metrics and the client's " +
						"measurements disagree, the client is the one telling the truth.",
				},
				Emits: []string{"k6_http_reqs_total", "k6_http_req_duration_p50/p95/p99", "k6_checks_rate"},
			},
			{
				ID:    "checkout-api",
				Name:  "checkout-api",
				Kind:  "service",
				Image: "Go 1.27, built here",
				Port:  "8080",
				Role:  "The service under test, and the thing you break.",
				Detail: []string{
					"A single endpoint, POST /checkout. It validates the order, pretends to do about 40ms of work, " +
						"and returns an order id. The fake work follows a log-normal distribution — a tight cluster " +
						"with a long tail to the right — because that is what real request latency looks like.",
					"It counts every request it serves, labelled by route, method and response code, and times every " +
						"one into a histogram. Those two numbers are the raw material for every SLI on this page.",
					"It also carries the fault engine. Faults are added through its admin API, they fire with a " +
						"probability, and they expire on their own. The service has no hidden failure modes — only " +
						"the ones you ask for.",
				},
				Emits: []string{"http_requests_total", "http_request_duration_seconds", "http_requests_in_flight", "faults_active"},
			},
			{
				ID:    "prometheus",
				Name:  "prometheus",
				Kind:  "infra",
				Image: "prom/prometheus",
				Port:  "9090",
				Role:  "The measuring instrument and the memory.",
				Detail: []string{
					"Every 5 seconds it asks checkout-api for its counters and stores them with a timestamp. That " +
						"5-second interval is deliberate: the SLO windows here are minutes long, and a slower scrape " +
						"would not leave enough samples for a short burn rate to mean anything.",
					"It answers questions in PromQL, a query language built around rates of change. Almost everything " +
						"on this page is the same shape of question: of all the requests in the last few minutes, what " +
						"fraction were good?",
					"It also accepts the load generator's pushed measurements, so both the inside view and the outside " +
						"view of the same traffic live in one place and can be compared directly.",
				},
			},
			{
				ID:    "console",
				Name:  "console",
				Kind:  "infra",
				Image: "Go 1.27, built here",
				Port:  "8090",
				Role:  "This page.",
				Detail: []string{
					"It asks Prometheus the SLI questions a couple of times a second and draws the answers, and it " +
						"forwards your fault injections to the service's admin API.",
					"It exists mostly to dodge a browser restriction: a plain web page cannot freely call several " +
						"different origins. Routing everything through one small server sidesteps that, and has a " +
						"pleasant side effect — every query lives in one place, so the page can show you the exact " +
						"PromQL behind each number instead of asking you to trust it.",
					"It holds no state. Restart it and nothing is lost, because the experiment lives in Prometheus " +
						"and the faults live in the service.",
				},
			},
			{
				ID:    "alertmanager",
				Name:  "alertmanager",
				Kind:  "infra",
				Image: "prom/alertmanager",
				Port:  "9093",
				Role:  "Turns a firing rule into a notification.",
				Detail: []string{
					"Prometheus decides that a rule is firing. Alertmanager decides what to do about it: " +
						"group related alerts into one notification, drop a ticket that a page already covers, " +
						"and route it somewhere.",
					"Both halves are on the control room. The alert list comes straight from Prometheus and " +
						"shows everything that is pending or firing; the notification list shows what actually " +
						"got delivered here. They are deliberately not the same set — the difference is " +
						"grouping and inhibition doing their job.",
					"In this workbench the route ends at the console itself, which just records what arrived.",
				},
			},
			{
				ID:    "grafana",
				Name:  "grafana",
				Kind:  "infra",
				Image: "grafana/grafana",
				Port:  "3000",
				Role:  "The conventional dashboards.",
				Detail: []string{
					"The same data as this page, in the tool most teams actually use. Useful for looking further back " +
						"in time, for zooming into a spike, and for comparing what a standard RED dashboard shows " +
						"against what an SLO view shows.",
					"A dashboard answers 'what is happening?'. An error budget answers 'do we need to do something " +
						"about it?'. Both views are here so the difference is easy to feel.",
				},
			},
		},

		Edges: []edge{
			{From: "loadgen", To: "checkout-api", Label: "~20 HTTP req/s"},
			{From: "prometheus", To: "checkout-api", Label: "scrapes /metrics every 5s"},
			{From: "loadgen", To: "prometheus", Label: "pushes client-side metrics"},
			{From: "console", To: "prometheus", Label: "PromQL queries"},
			{From: "console", To: "checkout-api", Label: "injects faults"},
			{From: "grafana", To: "prometheus", Label: "PromQL queries"},
			{From: "prometheus", To: "alertmanager", Label: "fires burn-rate alerts"},
			{From: "alertmanager", To: "console", Label: "webhook notification"},
		},

		Journey: []journeyStep{
			{
				N: 1, Where: "loadgen", Title: "A customer arrives",
				Detail: "k6 sends a checkout request. About one in fifty is deliberately malformed, because real users " +
					"send nonsense too and your monitoring has to have an opinion about that.",
			},
			{
				N: 2, Where: "checkout-api", Title: "The stopwatch starts",
				Detail: "Before any logic runs, the metrics middleware works out which route this is, starts a timer and " +
					"increments the in-flight counter. It resolves the route up front on purpose: if a fault rejects the " +
					"request later, the measurement must still know which endpoint was affected.",
			},
			{
				N: 3, Where: "checkout-api", Title: "The fault engine gets a say",
				Detail: "Active faults are checked. A latency fault sleeps for its delay. An error fault answers " +
					"immediately with its status code and the real handler is never reached. If both are active, the " +
					"delay is paid first — a request that times out is slow and then fails, not instantly broken.",
			},
			{
				N: 4, Where: "checkout-api", Title: "The handler does its work",
				Detail: "An invalid order gets a 400 and stops here. A valid one sleeps for its simulated work — around " +
					"40ms, occasionally much longer — and comes back with an order id and a 200.",
			},
			{
				N: 5, Where: "checkout-api", Title: "The measurement is written down",
				Detail: "On the way out, two things are recorded: one tick on a counter labelled with the route, method " +
					"and status code, and the elapsed time dropped into a histogram bucket. Nothing is stored per " +
					"request — just counters, which is why this scales to any traffic level.",
			},
			{
				N: 6, Where: "prometheus", Title: "The counters are collected",
				Detail: "Five seconds later Prometheus reads those counters and files them away with a timestamp. " +
					"Meanwhile k6 pushes what it saw from the outside, so the same traffic now exists in two versions.",
			},
			{
				N: 7, Where: "console", Title: "The counters become a ratio",
				Detail: "This page asks: over the last five minutes, how many requests failed, divided by how many there " +
					"were? That fraction is the SLI. Every card above is a variation on that one question.",
			},
			{
				N: 8, Where: "console", Title: "The ratio becomes a decision",
				Detail: "Compare the SLI against its target and you have an SLO. Whatever slack the target leaves — 1% of " +
					"requests, here — is the error budget: a concrete, spendable allowance for failure, and the number " +
					"that turns an argument about reliability into arithmetic.",
			},
			{
				N: 9, Where: "prometheus", Title: "The budget decides who to wake up",
				Detail: "Prometheus evaluates the same ratio over several windows at once and compares each against a " +
					"burn rate. Spending the budget 14.4 times faster than sustainable is a page; spending it at 1x is a " +
					"ticket. Two windows must agree before anything fires, so a spike that has already passed stops " +
					"alerting instead of ringing for hours.",
			},
			{
				N: 10, Where: "alertmanager", Title: "The notification is delivered",
				Detail: "Alertmanager groups the alerts for one SLO into a single notification, drops the ticket when a " +
					"page for the same budget is already out, and delivers what is left. The control room shows both " +
					"what is firing and what was delivered, because the two lists differ for interesting reasons.",
			},
		},

		Baseline: []fact{
			{Label: "Traffic", Value: "~20 req/s", Note: "Constant arrival rate, so budget arithmetic stays predictable."},
			{Label: "Invalid requests", Value: "~2%", Note: "Deliberate 400s. They never count against availability."},
			{Label: "Server errors", Value: "0%", Note: "Nothing fails unless you make it fail."},
			{Label: "Typical latency", Value: "~40ms, p99 ~150ms", Note: "Log-normal: a tight body and a long tail."},
			{Label: "Scrape interval", Value: "5s", Note: "Short windows need dense samples."},
			{Label: "SLO window", Value: "1 hour", Note: "Compressed from the usual 28 days so experiments finish in minutes."},
			{Label: "Availability target", Value: "99.0%", Note: "Leaves a 1% error budget: about 720 requests per hour."},
			{Label: "Latency target", Value: "95% under 300ms", Note: "Leaves a 5% budget of slow requests."},
		},

		Vocabulary: []term{
			{
				Term:  "SLI",
				Plain: "A service level indicator: one number that says what fraction of things went well.",
				Here:  "Of all checkout requests in the last 5 minutes, the share that did not return a 5xx — and, separately, the share served in under 300ms.",
			},
			{
				Term:  "SLO",
				Plain: "A service level objective: the target you hold that number to, over a stated window.",
				Here:  "99% available and 95% under 300ms, both measured over a rolling 1-hour window.",
			},
			{
				Term:  "Error budget",
				Plain: "The failure the objective allows. If you aim for 99%, then 1% is not a flaw — it is an allowance you are free to spend.",
				Here:  "About 720 failed requests per hour. Spend them on a risky deploy, or lose them to an incident; either way, when they are gone you stop shipping and start fixing.",
			},
			{
				Term:  "Burn rate",
				Plain: "How many times faster than sustainable you are spending the budget right now.",
				Here:  "A burn rate of 1 uses the whole hour's budget in exactly an hour. A burn rate of 30 uses it in two minutes — which is precisely what the first scenario does.",
			},
			{
				Term:  "Multi-window alert",
				Plain: "An alert that only fires when a fast window and a slow window agree that the budget is burning.",
				Here: "The long window decides whether it matters; the short one decides whether it is still happening. " +
					"Without the short window an alert keeps ringing long after the incident ended.",
			},
			{
				Term:  "Bad event",
				Plain: "A single occurrence that counts against you. Deciding what qualifies is the real work of defining an SLI.",
				Here:  "A 5xx counts. A 400 does not: the user sent something invalid and the service correctly told them so.",
			},
			{
				Term:  "RED",
				Plain: "Rate, errors, duration — the three things worth measuring about any request-driven service.",
				Here:  "The throughput, availability and latency cards. RED tells you what is happening; the error budget tells you whether to care.",
			},
			{
				Term:  "Histogram bucket",
				Plain: "Instead of storing every request's duration, count how many finished under each of a few thresholds.",
				Here:  "One of those thresholds is exactly 300ms, which is why the latency SLI can be computed as a simple ratio of counts rather than an estimated percentile.",
			},
		},

		InjectionPoints: []injectionPoint{
			{
				Fault:       "Errors",
				Where:       "Inside checkout-api, after the stopwatch starts and before the handler runs.",
				Effect:      "The chosen share of requests get a status code instead of a response. The handler never sees them.",
				VisibleIn:   []string{"Availability SLI", "Error budget", "Burn rate", "Requests by code"},
				InvisibleIn: []string{"Latency SLI — a request that fails instantly is, technically, very fast"},
			},
			{
				Fault:       "Latency",
				Where:       "Same place, just before the handler. The request is delayed and then served normally.",
				Effect:      "The chosen share of requests take longer. Nothing fails.",
				VisibleIn:   []string{"Latency SLI", "Latency budget", "p99", "In flight"},
				InvisibleIn: []string{"Availability SLI — it stays at a perfect 100% while users wait"},
			},
			{
				Fault:       "Crash",
				Where:       "The process itself. No TTL, nothing to undo.",
				Effect:      "The service dies and is restarted automatically, with all its counters back at zero.",
				VisibleIn:   []string{"Client-side throughput (k6) keeps going", "Prometheus target goes down"},
				InvisibleIn: []string{"The service's own metrics — a dead process cannot report its own outage, which is the entire reason to measure from the client too"},
			},
		},
	}
}
