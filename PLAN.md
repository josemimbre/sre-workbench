# SRE Workbench — an SLI/SLO playground

A local bench for **designing, measuring and breaking** SLIs and SLOs. The goal is not
to build good services: it is to build *controllably bad* ones, so we can watch what
happens to an error budget when things fail in specific, repeatable ways.

Conceptual reference: *Site Reliability Engineering* (ch. 4) and *The SRE Workbook*
(ch. 2 "Implementing SLOs" and ch. 5 "Alerting on SLOs").

---

## 1. Goals

When this is done, the repo should answer questions like these empirically:

- How much error budget does a 2-minute outage at 30% error rate actually cost?
- How can an availability SLI sit at 100% while users are unhappy?
- Which alert fires first, the fast burn rate or the long window? How noisy is each?
- Why doesn't the server-measured SLI match the client-measured one?
- What happens to p99 latency when a connection pool saturates?
- When does a *freshness* SLI make more sense than an availability one?

Non-goals: real high availability, security, serious persistence, multi-tenancy.

## 2. Design principles

1. **Every failure is explicit and reversible.** Nothing fails "by accident": faults are
   injected through an API, with a probability and a TTL, and they switch themselves off.
2. **Compressed time.** A 28-day SLO gives no feedback within an afternoon. Every SLO is
   defined with two windows: `playground` (1h/6h) for iterating, and `real` (28d) for
   learning the numbers people actually use.
3. **Specify the SLI in prose before PromQL.** Every SLI has a *specification* (what it
   means to a user) and then an *implementation* (the query).
4. **One service, one failure model.** No three clones: each service exists to illustrate
   a different family of SLI.
5. **Reproducible from scratch:** `make up` brings everything online, `make scenario-N`
   runs a full game day with no manual steps.

## 3. Architecture

```
                 ┌──────────┐
    k6 (loadgen) │  client  │  ── measures the *client-side* SLI (ground truth)
                 └────┬─────┘
                      │ HTTP
        ┌─────────────▼──────────────┐
        │        checkout-api        │  SLI: availability + latency
        └──────┬──────────────┬──────┘
               │ HTTP         │ enqueue
        ┌──────▼──────┐   ┌───▼────────────┐
        │ catalog-api │   │ (Redis queue)  │
        └──────┬──────┘   └───┬────────────┘
               │ SQL          │
        ┌──────▼──────┐   ┌───▼─────────────┐
        │  postgres   │   │ payments-worker │  SLI: freshness + correctness
        └─────────────┘   └─────────────────┘

  Everything exposes /metrics  ──►  Prometheus ──► Grafana
                                         │
                                         └──────► Alertmanager ──► webhook (log)
```

Fixed stack: **Go** (local toolchain for `go test`/`go run` while developing, multi-stage
Docker builds to run the environment), **Prometheus**, **Grafana**, **Alertmanager**,
**Redis**, **Postgres**, **k6**. SLO rules are generated with **Sloth**, which turns a
short YAML spec into recording rules plus multi-window alerts.

## 4. Service catalogue

| Service | What it does | Family of SLI it illustrates |
|---|---|---|
| `checkout-api` | Write endpoint. Calls `catalog-api` and enqueues a payment. | **Request/response**: availability and latency. The classic case. |
| `catalog-api` | Reads from Postgres with an in-memory cache. Deliberately small connection pool. | **Saturation and dependencies**: how a slow backend turns into errors upstream. |
| `payments-worker` | Consumes the Redis queue, processes with artificial delay. | **Data pipeline**: freshness, throughput and correctness. No HTTP involved. |
| `loadgen` (k6) | Synthetic traffic with a diurnal pattern and spikes. Exposes its own metrics. | **Client-side SLI**, to contrast with the server-side one. |

Every Go service shares `pkg/`: the RED metrics middleware, the fault engine and the
admin server. Each service's business logic should fit in ~150 lines.

## 5. Fault injection

A shared engine in `pkg/faults`, driven at runtime with no restart:

```
GET    /admin/faults        → current state
POST   /admin/faults        → add or replace a fault
DELETE /admin/faults/{id}   → remove it
```

A fault is `{type, scope (route/method), probability, parameters, ttl}`. The TTL is
mandatory: no fault outlives its experiment, so a forgotten injection cannot contaminate
the next run.

| Type | Parameters | What it is for |
|---|---|---|
| `latency` | `delay`, `jitter`, `p` | Degradation that leaves availability intact but breaks the latency SLO. |
| `error` | `status`, `p` | Predictable, measurable budget burn. |
| `dependency_slow` | `target`, `delay` | Latency that propagates upstream. |
| `dependency_down` | `target` | Hard dependency failure: graceful degradation or full outage? |
| `pool_squeeze` | `max_conns` | Saturation: queueing, timeouts and a p99 that explodes. |
| `cpu_burn` | `cores`, `duty` | Noisy neighbour: hits every endpoint at once. |
| `leak` | `mb_per_min` | Slow degradation ending in OOM. Good material for long windows. |
| `stall` | `duration` | The worker stops consuming: HTTP availability perfect, freshness destroyed. |
| `poison` | `p` | Wrong results: breaks the correctness SLI while RED stays clean. |
| `crash` | — | Restart; in the kind phase, CrashLoopBackOff. |

`scenarios/` stores each experiment as a script that orchestrates injection, waiting and
verification, so runs are repeatable and comparable.

## 6. Initial SLIs and SLOs

Specification first, implementation second.

### 6.1 `checkout-api` — availability

- **Specification:** the proportion of requests to `/checkout` that do not return 5xx.
- **Implementation:** bad events are `code=~"5.."` over the total, per service.
- **Target:** 99.0% (playground) / 99.9% (real).

```promql
sum(rate(http_requests_total{job="checkout-api",route="/checkout",code=~"5.."}[5m]))
/
sum(rate(http_requests_total{job="checkout-api",route="/checkout"}[5m]))
```

### 6.2 `checkout-api` — latency

- **Specification:** the proportion of requests served in under 300 ms.
- **Implementation:** a ratio of histogram buckets. *Not* a quantile: quantiles cannot be
  aggregated, and they cannot be turned into an error budget.
- **Target:** 95% under 300 ms.

```promql
sum(rate(http_request_duration_seconds_bucket{job="checkout-api",le="0.3"}[5m]))
/
sum(rate(http_request_duration_seconds_count{job="checkout-api"}[5m]))
```

### 6.3 `catalog-api` — availability with dependencies

Same shape as §6.1, but the interesting part is deciding whether a 503 caused by pool
saturation counts as a bad event (it does) and whether a legitimate 404 does (it doesn't).
That decision is an explicit exercise of this plan.

### 6.4 `payments-worker` — freshness

- **Specification:** the proportion of payments processed within 60 s of being enqueued.
- **Implementation:** a `job_queue_delay_seconds` histogram with an `le="60"` bucket.
- **Target:** 99% (playground).

### 6.5 `payments-worker` — correctness

- **Specification:** the proportion of payments with a correct result, verified by a
  checker that recomputes the amount.
- **Implementation:** `payments_processed_total{result="incorrect"}` over the total.
- **Why it matters:** the `poison` fault leaves the RED dashboards spotless.

## 7. Error budget and alerting

For each SLO, Sloth generates the recording rules (`slo:sli_error:ratio_rate5m`, `30m`,
`1h`, `2h`, `6h`, `1d`, `3d`) and the four multi-window alerts from the SRE Workbook:

| Severity | Burn rate | Long window | Short window | Budget consumed when it fires |
|---|---|---|---|---|
| page | 14.4× | 1h | 5m | 2% |
| page | 6× | 6h | 30m | 5% |
| ticket | 3× | 1d | 2h | 10% |
| ticket | 1× | 3d | 6h | 10% |

The short window acts as confirmation: it keeps an alert from staying lit for hours after
the incident is over.

Mandatory Grafana dashboard per service: current SLI, remaining budget as a percentage,
budget burned in the window, current burn rate, and projected exhaustion time.

## 8. Scenarios (game days)

Each one is a script in `scenarios/`, with its hypothesis written down **before** it runs.

| # | Scenario | Injection | What it teaches |
|---|---|---|---|
| 1 | Short blip | `error p=0.3`, 2 min | What a brief incident costs in budget, and which alert catches it. |
| 2 | Slow degradation | `latency +150ms`, 6h | Perfect availability, broken latency SLO. Long-window alerting. |
| 3 | Slow dependency | `dependency_slow` on catalog | Cascade: latency below → saturation → errors above. |
| 4 | Pool saturation | `pool_squeeze max_conns=2` | Queueing, timeouts, non-linear p99. Why averages lie. |
| 5 | Bad deploy | `error p=0.02` on `version="v2"` only | SLI segmented by version; catching a partial failure. |
| 6 | Retry storm | aggressive client retries + `latency` | Load amplification and correlated failure. |
| 7 | Stalled worker | `stall 10m` | HTTP at 100%, freshness on the floor. Why data SLIs exist. |
| 8 | Corrupt data | `poison p=0.05` | Correctness: the SLI no RED dashboard can see. |
| 9 | Budget exhausted | chain 1 + 2 | Deploy-freeze policy; making the call with data. |
| 10 | Platform failure | (kind phase) pod kill, OOM, cordon | App failure vs infra failure through the same SLI. |

Cross-cutting exercise: in every scenario, compare the server-side SLI against k6's.

## 9. Phases

| Phase | Deliverable | Done when… |
|---|---|---|
| **0 — Skeleton** | Compose with Prometheus + Grafana and a minimal `checkout-api` exposing `/metrics`. | `make up` shows the RED dashboard with k6 traffic on it. |
| **1 — Services and faults** | The three services, `pkg/faults`, the admin API, and a loadgen with a diurnal pattern. | Any fault from §5 can be injected with `curl` and seen in Grafana. |
| **2 — SLIs/SLOs** | Sloth specs, recording rules, error budget dashboard. | A scenario shows the budget draining in real time. |
| **3 — Alerting and game days** | Multi-window alerts, Alertmanager, runbooks, scenarios 1–9 scripted. | `make scenario-1` runs, alerts, and reports the budget consumed. |
| **4 — Kubernetes** | kind plus kube-prometheus-stack, the same services, scenario 10. | The same SLOs work on k8s without being rewritten. |
| **5 — Extras (optional)** | OTel traces with Tempo, native histograms, Pyrra as an SLO UI. | — |

## 10. Repository layout

```
sre-workbench/
├── PLAN.md
├── Makefile                  # up, down, scenario-N, slo-gen, lint
├── services/
│   ├── checkout-api/
│   ├── catalog-api/
│   ├── payments-worker/
│   └── pkg/
│       ├── faults/           # injection engine + admin API
│       ├── metrics/          # RED middleware, shared histograms
│       └── httpx/            # client with timeouts and (optional) retries
├── deploy/
│   ├── compose/              # phases 0–3
│   └── k8s/                  # phase 4 (kind)
├── observability/
│   ├── prometheus/
│   ├── alertmanager/
│   ├── grafana/dashboards/
│   └── slo/                  # Sloth specs → generated rules
├── loadgen/                  # k6 scripts
└── scenarios/                # one script and one runbook per game day
```

## 11. Decisions already made

- **Go** for the services: a single module under `services/` with a shared `pkg/`.
  Development uses the local toolchain (`go test ./...`, `go run`); the full environment
  runs from multi-stage images (`golang:alpine` → `distroless/static`).
- **Docker Compose** for phases 0–3; **kind** in phase 4, not before.
- **Bucket ratios, never quantiles**, for latency SLIs.
- **Sloth** generates the rules instead of us hand-writing seven recording rules per SLO.
- **Two SLO windows** (playground 1h/6h, real 28d) so experiments give feedback in minutes.

## 12. Open questions

- Is a `gateway` in front worth it, to measure the SLI at the edge, closer to the real
  user? (Probably yes, in phase 3.)
- Should business metrics be SLIs too (completed payments per minute), alongside the
  technical ones?
- Should we model dependencies between SLOs (checkout's SLO depends on catalog's)?
- Should there be a *coverage* SLI for the measurement system itself: what happens when
  Prometheus goes down during an incident?
