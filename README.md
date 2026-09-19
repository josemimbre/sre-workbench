# SRE Workbench

A local playground for designing, measuring and **deliberately breaking** SLIs and SLOs.
The full plan lives in [PLAN.md](PLAN.md); this is just how to run it.

Status: **phase 0** — skeleton with one service, RED metrics and a dashboard.

## Getting started

```bash
make up      # build and start everything
make ps      # container status
make down    # stop (use make clean to drop the stored series too)
```

| What | Where |
|---|---|
| RED dashboard | http://localhost:3000/d/workbench-red |
| Prometheus | http://localhost:9090 |
| checkout-api | http://localhost:8080/checkout |

Grafana allows anonymous access as Admin; `admin` / `admin` if you ever need it.

A minute after `make up` you should see roughly 20 req/s, about 2% of 400s (invalid
requests the load generator sends on purpose) and **zero** 5xx. That is the baseline the
injected faults of phase 1 will be measured against.

## What is in here

- `services/` — a single Go module. `checkout-api` is the only service so far;
  `pkg/metrics` holds the RED middleware every service will share.
- `observability/` — Prometheus configuration and Grafana provisioning.
  `prometheus/rules/` is empty, waiting for the phase 2 SLO rules.
- `loadgen/` — the k6 script. It ships its metrics to Prometheus over remote write, so
  the SLI can be compared *server-side* against *client-side*.
- `scenarios/` — empty until phase 3 (game days).

## Useful commands

```bash
make smoke                    # a manual request against /checkout
make logs S=checkout-api
make test                     # Go tests
make check                    # fmt + vet + build
make restart S=checkout-api   # rebuild one service after changing its code
```

## Decisions worth knowing about

- The `scrape_interval` is **5s**: playground SLO windows are minutes long, and at 15s
  there would not be enough samples for a 5m burn rate to mean anything.
- Services do **not** emit a `service` label: identity comes from Prometheus' `job`, and
  emitting both would cause a relabeling conflict.
- `/metrics` is served outside the middleware: scraping is not user traffic and would
  pollute the SLI.
- 4xx responses are **not** bad events. The load generator sends 2% invalid requests
  precisely so that this stays visible on every dashboard.
