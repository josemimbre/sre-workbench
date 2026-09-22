# SRE Workbench

A local playground for designing, measuring and **deliberately breaking** SLIs and SLOs.

One service is wired end to end: RED metrics, faults you can inject on demand, SLO
recording rules, multi-window burn-rate alerts, and a console that explains and drives
all of it. Two more services — one for saturation and dependencies, one for a data
pipeline's freshness and correctness — are not built yet.

## Getting started

```bash
make up      # build and start everything
make ps      # container status
make down    # stop (use make clean to drop the stored series too)
```

| What | Where |
|---|---|
| **Console** — start here | http://localhost:8090 |
| RED dashboard | http://localhost:3000/d/workbench-red |
| Prometheus | http://localhost:9090 |
| Alertmanager | http://localhost:9093 |
| checkout-api | http://localhost:8080/checkout |

Grafana allows anonymous access as Admin; `admin` / `admin` if you ever need it.

A minute after `make up` you should see roughly 20 req/s, about 2% of 400s (invalid
requests the load generator sends on purpose) and **zero** 5xx. That is the baseline every
injected fault is measured against.

## What is in here

- `services/` — a single Go module. `checkout-api` is the only service so far;
  `pkg/metrics` holds the RED middleware every service will share.
- `services/console/` — the control plane: it proxies Prometheus and the services' admin
  APIs from one origin, holds every SLI query, and serves its own UI from inside the Go
  binary. No build step, no node_modules.
- `observability/slo/` — the SLO definitions. `make slo-gen` turns them into the
  recording rules and burn-rate alerts under `observability/prometheus/rules/`, which are
  committed so the stack comes up without running a generator.
- `observability/` — Prometheus, Alertmanager and Grafana configuration.
- `loadgen/` — the k6 script. It ships its metrics to Prometheus over remote write, so
  the SLI can be compared *server-side* against *client-side*.

Scenarios — the scripted game days — are defined in the console's catalog and run from
its UI, not from files on disk.

## Useful commands

```bash
make smoke                    # a manual request against /checkout
make logs S=checkout-api
make test                     # Go tests
make check                    # fmt + vet + build
make restart S=checkout-api   # rebuild one service after changing its code
make slo-gen                  # regenerate SLO rules after editing observability/slo
make reload                   # reload Prometheus config without restarting it
```

## Trying it

Open the console, read **Overview** to see what is running, then go to the **Control
room** and run the *Short blip* scenario: 30% of requests fail for two minutes. Watch the
burn rate climb past 14.4, the page alert fire, and Alertmanager deliver one notification
instead of two — the ticket for the same budget is inhibited by the page.

## Decisions worth knowing about

- The `scrape_interval` is **5s**: playground SLO windows are minutes long, and at 15s
  there would not be enough samples for a 5m burn rate to mean anything.
- Services do **not** emit a `service` label: identity comes from Prometheus' `job`, and
  emitting both would cause a relabeling conflict.
- `/metrics` is served outside the middleware: scraping is not user traffic and would
  pollute the SLI.
- 4xx responses are **not** bad events. The load generator sends 2% invalid requests
  precisely so that this stays visible on every dashboard.
- The SLO period is **1 hour**, not the usual 30 days, so an experiment gives feedback in
  minutes. The alert windows are compressed to match, which has a floor: see
  `observability/slo/windows/1h.yaml` for the arithmetic and what it costs.
