COMPOSE := docker compose -f deploy/compose/compose.yml

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: up
up: ## Start the whole environment (build included)
	$(COMPOSE) up -d --build
	@echo
	@echo "  console       http://localhost:8090   <- start here"
	@echo "  checkout-api  http://localhost:8080/checkout"
	@echo "  prometheus    http://localhost:9090"
	@echo "  grafana       http://localhost:3000/d/workbench-red"

.PHONY: down
down: ## Stop the environment (data is kept)
	$(COMPOSE) down

.PHONY: clean
clean: ## Stop the environment and drop the volumes (stored series included)
	$(COMPOSE) down -v

.PHONY: ps
ps: ## Container status
	$(COMPOSE) ps

.PHONY: logs
logs: ## Follow the logs (S=service to filter)
	$(COMPOSE) logs -f $(S)

.PHONY: restart
restart: ## Rebuild and restart one service (S=checkout-api)
	$(COMPOSE) up -d --build $(S)

.PHONY: smoke
smoke: ## Send one test request to checkout-api
	curl -sS -X POST localhost:8080/checkout -H 'Content-Type: application/json' \
		-d '{"item_id":"sku-1","qty":2}' | python3 -m json.tool

.PHONY: reload
reload: ## Reload the Prometheus configuration without restarting it
	curl -sS -X POST localhost:9090/-/reload && echo "prometheus reloaded"

.PHONY: test
test: ## Run the Go tests
	cd services && go test ./...

.PHONY: check
check: ## fmt + vet + build of the services
	cd services && gofmt -l . && go vet ./... && go build ./...

.PHONY: slo-gen
slo-gen: ## Regenerate the SLO recording rules and alerts from observability/slo
	docker run --rm --user "$$(id -u):$$(id -g)" -v "$(PWD)/observability:/obs" \
		ghcr.io/slok/sloth:latest generate \
		-i /obs/slo/checkout-api.yml \
		-o /obs/prometheus/rules/checkout-api.yml \
		--slo-period-windows-path /obs/slo/windows \
		--default-slo-period 1h
	$(MAKE) reload
