# Alcove Makefile — Build, test, and local development targets.

MODULE   := github.com/bmbouter/alcove
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -ldflags="-s -w -X main.Version=$(VERSION)"
BINDIR   := bin
INTERNAL_NET := alcove-internal
EXTERNAL_NET := alcove-external

comma := ,
REGISTRY     ?= ghcr.io/bmbouter
GHCR_USER    ?= $(USER)
IMAGES       := bridge gate skiff-base

GO       := go
PODMAN   := podman

CMDS     := bridge gate skiff-init alcove debug-env

.PHONY: all build build-cli-all build-images build-image-bridge build-image-gate build-image-skiff-base build-skiff \
        test test-network test-ledger test-isolation test-schedules test-credentials test-security-profiles test-yaml-security-profiles test-gate-real lint clean \
        up down logs watch dev-config dev-up dev-down dev-logs dev-reset dev-infra help \
        login-registry push pull up-pull build-tooling push-tooling

all: build

##@ Build

build: ## Build all Go binaries locally
	@mkdir -p $(BINDIR)
	@for cmd in $(CMDS); do \
		echo "Building $$cmd..."; \
		$(GO) build $(LDFLAGS) -o $(BINDIR)/$$cmd ./cmd/$$cmd; \
	done
	@echo "Binaries written to $(BINDIR)/"

build-cli-all: ## Build CLI for all platforms (Linux, macOS, Windows, AMD64/ARM64)
	@mkdir -p dist
	@echo "Building alcove CLI for all platforms..."
	@for platform in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do \
		export GOOS=$${platform%/*} GOARCH=$${platform#*/}; \
		ext=""; [ "$$GOOS" = "windows" ] && ext=".exe"; \
		echo "  Building for $$GOOS/$$GOARCH..."; \
		CGO_ENABLED=0 $(GO) build $(LDFLAGS) \
			-o "dist/alcove-$$GOOS-$$GOARCH$$ext" ./cmd/alcove; \
	done
	@cd dist && sha256sum alcove-* > checksums-sha256.txt
	@echo "Cross-platform CLI binaries written to dist/"

build-images: build-image-bridge build-image-gate build-image-skiff-base ## Build all container images with podman

build-image-bridge:
	$(PODMAN) build --build-arg VERSION=$(VERSION) -f build/Containerfile.bridge -t localhost/alcove-bridge:$(VERSION) .

build-image-gate:
	$(PODMAN) build --build-arg VERSION=$(VERSION) -f build/Containerfile.gate -t localhost/alcove-gate:$(VERSION) .

build-image-skiff-base:
	$(PODMAN) build --build-arg VERSION=$(VERSION) -f build/Containerfile.skiff-base -t localhost/alcove-skiff-base:$(VERSION) .

build-skiff: build ## Rebuild only the Skiff base image (after changing debug-env or skiff-init)
	$(PODMAN) build --build-arg VERSION=$(VERSION) -f build/Containerfile.skiff-base -t localhost/alcove-skiff-base:$(VERSION) .

##@ Easy Targets

up: dev-config dev-infra build ## Build locally and start Bridge + infra (~20s)
	@echo "Starting Bridge locally..."
	@LEDGER_DATABASE_URL="postgres://alcove:alcove@localhost:5432/alcove?sslmode=disable" \
	HAIL_URL="nats://localhost:4222" \
	RUNTIME=podman \
	ALCOVE_NETWORK=$(INTERNAL_NET) \
	ALCOVE_EXTERNAL_NETWORK=$(EXTERNAL_NET) \
	nohup $(BINDIR)/bridge > /tmp/alcove-bridge.log 2>&1 &
	@sleep 2
	@echo ""
	@echo "Dashboard:   http://localhost:8080"
	@echo "NATS:        nats://localhost:4222 (monitoring: http://localhost:8222)"
	@echo "PostgreSQL:  postgres://alcove:alcove@localhost:5432/alcove"
	@echo "Bridge logs: /tmp/alcove-bridge.log"

up-full: build-images dev-up ## Build container images and start everything in containers (~8min)

down: ## Stop everything
	-@pkill -f '$(BINDIR)/bridge' 2>/dev/null || true
	-@$(PODMAN) stop alcove-bridge 2>/dev/null || true
	-@$(PODMAN) stop alcove-hail 2>/dev/null || true
	-@$(PODMAN) stop alcove-ledger 2>/dev/null || true
	-@$(PODMAN) network rm $(INTERNAL_NET) 2>/dev/null || true
	-@$(PODMAN) network rm $(EXTERNAL_NET) 2>/dev/null || true
	@echo "Dev environment stopped."

logs: ## Show Bridge logs
	@if [ -f /tmp/alcove-bridge.log ]; then tail -50 /tmp/alcove-bridge.log; \
	else $(PODMAN) logs --tail 50 alcove-bridge 2>/dev/null || echo "No logs found"; fi

watch: dev-config dev-infra  ## Run Bridge with hot-reload (auto-restart on code changes)
	LEDGER_DATABASE_URL="postgres://alcove:alcove@localhost:5432/alcove?sslmode=disable" \
	HAIL_URL="nats://localhost:4222" \
	RUNTIME=podman \
	ALCOVE_NETWORK=$(INTERNAL_NET) \
	ALCOVE_EXTERNAL_NETWORK=$(EXTERNAL_NET) \
	air

##@ Development

dev-config: ## Generate alcove.yaml from example if it does not exist
	@if [ ! -f alcove.yaml ]; then \
		echo "Generating alcove.yaml with a random database_encryption_key..."; \
		sed "s/change-me-to-a-strong-secret/$$(openssl rand -hex 32)/" alcove.yaml.example > alcove.yaml; \
		echo "Created alcove.yaml — edit as needed."; \
	fi

dev-up: dev-config ## Start full containerized environment
	@echo "Creating podman networks..."
	-$(PODMAN) network create --internal $(INTERNAL_NET) 2>/dev/null || true
	-$(PODMAN) network create $(EXTERNAL_NET) 2>/dev/null || true
	@echo "Starting ledger (PostgreSQL) on internal network..."
	$(PODMAN) run -d --rm --replace \
		--name alcove-ledger \
		--network $(INTERNAL_NET) \
		-e POSTGRES_USER=alcove \
		-e POSTGRES_PASSWORD=alcove \
		-e POSTGRES_DB=alcove \
		-p 5432:5432 \
		docker.io/library/postgres:16
	@echo "Starting hail (NATS) on internal network..."
	$(PODMAN) run -d --rm --replace \
		--name alcove-hail \
		--network $(INTERNAL_NET) \
		-p 4222:4222 \
		-p 8222:8222 \
		docker.io/library/nats:latest
	@echo "Waiting for services to be ready..."
	@sleep 3
	@echo "Starting bridge on internal network..."
	$(PODMAN) run -d --rm --replace \
		--name alcove-bridge \
		--network $(INTERNAL_NET)$(comma)$(EXTERNAL_NET) \
		-p 8080:8080 \
		--user $$(id -u):$$(id -g) \
		-v $${XDG_RUNTIME_DIR}/podman/podman.sock:/run/podman/podman.sock:z \
		-v $(CURDIR)/web:/web:ro,z \
		$(if $(wildcard alcove.yaml),-v $(CURDIR)/alcove.yaml:/etc/alcove/alcove.yaml:ro$(comma)z,) \
		-e CONTAINER_HOST=unix:///run/podman/podman.sock \
		-e LEDGER_DATABASE_URL=postgres://alcove:alcove@alcove-ledger:5432/alcove?sslmode=disable \
		-e HAIL_URL=nats://alcove-hail:4222 \
		-e RUNTIME=podman \
		-e ALCOVE_WEB_DIR=/web \
		-e ALCOVE_NETWORK=$(INTERNAL_NET) \
		-e ALCOVE_EXTERNAL_NETWORK=$(EXTERNAL_NET) \
		-e SKIFF_IMAGE=localhost/alcove-skiff-base:$(VERSION) \
		-e GATE_IMAGE=localhost/alcove-gate:$(VERSION) \
		localhost/alcove-bridge:$(VERSION)
	@echo ""
	@echo "Alcove is starting up. Fetching admin password..."
	@sleep 2
	@$(PODMAN) logs alcove-bridge 2>&1 | grep -A1 "BOOTSTRAP" || echo "(check 'make logs' for admin password)"
	@echo ""
	@echo "Dashboard:   http://localhost:8080"
	@echo "NATS:        nats://localhost:4222 (monitoring: http://localhost:8222)"
	@echo "PostgreSQL:  postgres://alcove:alcove@localhost:5432/alcove"
	@echo ""
	@echo "Network isolation: Skiff containers on $(INTERNAL_NET) (no internet)."
	@echo "                   Gate containers on $(INTERNAL_NET)+$(EXTERNAL_NET) (internet via proxy)."

dev-infra: ## Start only NATS + PostgreSQL (run Bridge locally with ./bin/bridge)
	@echo "Creating podman networks..."
	-$(PODMAN) network create --internal $(INTERNAL_NET) 2>/dev/null || true
	-$(PODMAN) network create $(EXTERNAL_NET) 2>/dev/null || true
	@echo "Starting ledger (PostgreSQL) on internal network..."
	$(PODMAN) run -d --replace \
		--name alcove-ledger \
		--network $(INTERNAL_NET) \
		-v alcove-ledger-data:/var/lib/postgresql/data \
		-e POSTGRES_USER=alcove \
		-e POSTGRES_PASSWORD=alcove \
		-e POSTGRES_DB=alcove \
		-p 5432:5432 \
		docker.io/library/postgres:16
	@echo "Starting hail (NATS) on internal network..."
	$(PODMAN) run -d --rm --replace \
		--name alcove-hail \
		--network $(INTERNAL_NET) \
		-p 4222:4222 \
		-p 8222:8222 \
		docker.io/library/nats:latest
	@echo ""
	@echo "Infrastructure is up. Run Bridge locally with:"
	@echo "  LEDGER_DATABASE_URL=\"postgres://alcove:alcove@localhost:5432/alcove?sslmode=disable\" \\"
	@echo "  HAIL_URL=\"nats://localhost:4222\" \\"
	@echo "  RUNTIME=podman \\"
	@echo "  ALCOVE_NETWORK=$(INTERNAL_NET) \\"
	@echo "  ALCOVE_EXTERNAL_NETWORK=$(EXTERNAL_NET) \\"
	@echo "  ./bin/bridge"

dev-down: ## Stop and remove all dev containers and networks
	-$(PODMAN) stop alcove-bridge 2>/dev/null
	-$(PODMAN) stop alcove-hail 2>/dev/null
	-$(PODMAN) stop alcove-ledger 2>/dev/null
	-$(PODMAN) network rm $(INTERNAL_NET) 2>/dev/null
	-$(PODMAN) network rm $(EXTERNAL_NET) 2>/dev/null
	@echo "Dev environment stopped."

dev-logs: ## Tail logs from all dev containers
	@echo "=== Bridge ===" && $(PODMAN) logs --tail 50 alcove-bridge 2>/dev/null; \
	echo "=== Hail ===" && $(PODMAN) logs --tail 50 alcove-hail 2>/dev/null; \
	echo "=== Ledger ===" && $(PODMAN) logs --tail 50 alcove-ledger 2>/dev/null

dev-reset: dev-down ## Stop containers and remove all volumes
	-$(PODMAN) volume rm alcove-ledger-data 2>/dev/null
	@echo "Dev environment reset (volumes removed)."

##@ Quality

test: ## Run all tests
	$(GO) test ./...

test-network: ## Smoke-test Skiff network isolation (requires podman + skiff-base image)
	@echo "Running network isolation smoke tests..."
	@./scripts/test-network-isolation.sh --internal

test-ledger: ## Test Ledger data access and ownership isolation (requires running Bridge with AUTH_BACKEND=postgres)
	@echo "NOTE: Requires running Bridge with AUTH_BACKEND=postgres and ADMIN_PASSWORD set"
	ADMIN_PASSWORD=$${ADMIN_PASSWORD:-admin} ./scripts/test-ledger-access.sh

test-isolation: ## Test user data isolation (requires running Bridge with AUTH_BACKEND=postgres)
	ADMIN_PASSWORD=$${ADMIN_PASSWORD:-alcove-admin-2026} ./scripts/test-user-isolation.sh

test-schedules: ## Test schedule CRUD and isolation
	ADMIN_PASSWORD=$${ADMIN_PASSWORD:-alcove-admin-2026} ./scripts/test-schedules.sh

test-credentials: ## Test credential CRUD and isolation
	ADMIN_PASSWORD=$${ADMIN_PASSWORD:-alcove-admin-2026} ./scripts/test-credentials.sh

test-security-profiles: ## Test AI security profile builder (requires system LLM configured)
	ADMIN_PASSWORD=$${ADMIN_PASSWORD:-alcove-admin-2026} ./scripts/test-profile-builder.sh

test-yaml-security-profiles: ## Test YAML security profile sync from task repos
	ADMIN_PASSWORD=$${ADMIN_PASSWORD:-alcove-admin-2026} ./scripts/test-yaml-security-profiles.sh

test-gate-real: ## Test Gate scope enforcement with real GitHub API (requires running Bridge + GitHub credential)
	ADMIN_PASSWORD=$${ADMIN_PASSWORD:-admin123} ./scripts/test-gate-real.sh

lint: ## Run linters (go vet + staticcheck)
	$(GO) vet ./...
	@which staticcheck >/dev/null 2>&1 && staticcheck ./... || \
		echo "staticcheck not installed; run: go install honnef.co/go/tools/cmd/staticcheck@latest"

##@ Cleanup

clean: ## Remove build artifacts
	rm -rf $(BINDIR)
	@echo "Cleaned."

##@ Registry

login-registry: ## Log in to ghcr.io (requires GHCR_TOKEN env var)
	@test -n "$$(echo $(GHCR_TOKEN))" || (echo "Error: set GHCR_TOKEN env var"; exit 1)
	@echo "$(GHCR_TOKEN)" | $(PODMAN) login ghcr.io -u $(GHCR_USER) --password-stdin
	@echo "Logged in to ghcr.io as $(GHCR_USER)."

push: build-images ## Tag and push all images to ghcr.io
	@for img in $(IMAGES); do \
		echo "Pushing alcove-$$img:$(VERSION)..."; \
		$(PODMAN) tag localhost/alcove-$$img:$(VERSION) $(REGISTRY)/alcove-$$img:$(VERSION); \
		$(PODMAN) tag localhost/alcove-$$img:$(VERSION) $(REGISTRY)/alcove-$$img:latest; \
		$(PODMAN) push $(REGISTRY)/alcove-$$img:$(VERSION); \
		$(PODMAN) push $(REGISTRY)/alcove-$$img:latest; \
	done
	@echo "All images pushed to $(REGISTRY)."

pull: ## Pull pre-built images from ghcr.io
	@for img in $(IMAGES); do \
		echo "Pulling $(REGISTRY)/alcove-$$img:$(VERSION)..."; \
		$(PODMAN) pull $(REGISTRY)/alcove-$$img:$(VERSION); \
		$(PODMAN) tag $(REGISTRY)/alcove-$$img:$(VERSION) localhost/alcove-$$img:$(VERSION); \
	done
	@echo "All images pulled and tagged locally."

up-pull: pull dev-up ## Pull pre-built images and start everything (no local build)

##@ Tooling Images

build-tooling:  ## Build the skiff tooling base image (heavy, rarely needed)
	$(PODMAN) build -f build/Containerfile.skiff-tooling -t localhost/alcove-skiff-tooling:latest .

push-tooling: build-tooling  ## Push skiff tooling base to ghcr.io
	$(PODMAN) tag localhost/alcove-skiff-tooling:latest $(REGISTRY)/alcove-skiff-tooling:latest
	$(PODMAN) push $(REGISTRY)/alcove-skiff-tooling:latest

##@ Help

help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)
