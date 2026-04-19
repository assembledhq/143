# Stamp file tracking when the sandbox image was last built.
SANDBOX_STAMP := sandbox/.build-stamp
SANDBOX_SOURCES := sandbox/Dockerfile sandbox/versions.json

.PHONY: dev dev-ngrok dev-local dev-frontend-only setup test test-race test-coverage test-pr test-coverage-diff test-main migrate-up migrate-down build frontend-dev frontend-lint frontend-typecheck frontend-check lint lint-bootstrap lint-schema lint-stores lint-tenancy hooks-install hooks-uninstall secrets-setup secrets-encrypt secrets-decrypt secrets-edit secrets-rotate provision-app provision-worker provision-db provision-logging deploy deploy-app deploy-worker deploy-db deploy-logging deploy-fleet logs

GOLANGCI_LINT_VERSION ?= v2.10.1
GOLANGCI_LINT_BIN := $(CURDIR)/bin/golangci-lint
GO_TOOLCHAIN_VERSION := $(shell go env GOVERSION)

dev:
	@FRONTEND_PORT=$${FRONTEND_PORT:-3000}; \
	if lsof -nP -iTCP:$$FRONTEND_PORT -sTCP:LISTEN >/dev/null 2>&1; then \
		echo "Port $$FRONTEND_PORT is already in use."; \
		echo "Stop the process on that port, or run: FRONTEND_PORT=3001 make dev"; \
		exit 1; \
	fi; \
	$(MAKE) sandbox-image; \
	docker compose up --build

# Only rebuild the sandbox image when Dockerfile or versions.json change.
$(SANDBOX_STAMP): $(SANDBOX_SOURCES)
	docker compose build sandbox
	@touch $@

sandbox-image: $(SANDBOX_STAMP)

# Run the full Docker stack with an ngrok tunnel for external access.
# The NGROK_DOMAIN is required (your reserved ngrok domain).
# This patches .env with the ngrok URL, starts ngrok + docker compose,
# and restores .env on exit.
#
# Usage:
#   make dev-ngrok NGROK_DOMAIN=assembled.ngrok.dev
dev-ngrok:
	@command -v ngrok >/dev/null 2>&1 || { echo "Install ngrok: brew install ngrok"; exit 1; }
	@if [ -z "$(NGROK_DOMAIN)" ]; then \
		echo "Error: NGROK_DOMAIN is required."; \
		echo ""; \
		echo "Usage: make dev-ngrok NGROK_DOMAIN=assembled.ngrok.dev"; \
		exit 1; \
	fi
	@FRONTEND_PORT=$${FRONTEND_PORT:-3000}; \
	if lsof -nP -iTCP:$$FRONTEND_PORT -sTCP:LISTEN >/dev/null 2>&1; then \
		echo "Port $$FRONTEND_PORT is already in use."; \
		echo "Stop the process on that port, or run: FRONTEND_PORT=3001 make dev-ngrok NGROK_DOMAIN=$(NGROK_DOMAIN)"; \
		exit 1; \
	fi; \
	NGROK_URL="https://$(NGROK_DOMAIN)"; \
	echo "Setting up ngrok tunnel: $$NGROK_URL → localhost:$$FRONTEND_PORT"; \
	cp .env .env.pre-ngrok; \
	sed -i '' \
		-e "s|^BASE_URL=.*|BASE_URL=$$NGROK_URL|" \
		-e "s|^FRONTEND_URL=.*|FRONTEND_URL=$$NGROK_URL|" \
		-e "s|^CORS_ALLOWED_ORIGINS=.*|CORS_ALLOWED_ORIGINS=$$NGROK_URL|" \
		.env; \
	echo "Patched .env with $$NGROK_URL"; \
	cleanup() { \
		echo ""; \
		echo "Restoring .env ..."; \
		mv .env.pre-ngrok .env; \
		echo "Done."; \
	}; \
	trap cleanup EXIT INT TERM; \
	ngrok http $$FRONTEND_PORT --url=$(NGROK_DOMAIN) > /tmp/ngrok-dev.log 2>&1 & \
	NGROK_PID=$$!; \
	sleep 3; \
	if ! kill -0 $$NGROK_PID 2>/dev/null; then \
		echo ""; \
		echo "ERROR: ngrok failed to start. Output:"; \
		cat /tmp/ngrok-dev.log; \
		exit 1; \
	fi; \
	echo "ngrok tunnel running (pid $$NGROK_PID)"; \
	BASE_URL=$$NGROK_URL \
	FRONTEND_URL=$$NGROK_URL \
	CORS_ALLOWED_ORIGINS=$$NGROK_URL \
	$(MAKE) sandbox-image; \
	docker compose up --build; \
	kill $$NGROK_PID 2>/dev/null; \
	wait $$NGROK_PID 2>/dev/null

# Run the full stack natively without Docker — lowest resource footprint.
# Requires Go, Node.js, and PostgreSQL installed locally (run: make setup first).
# Run each command in a separate terminal:
#   Terminal 1: make server-dev
#   Terminal 2: make frontend-dev
dev-local:
	@echo "Run each of the following in a separate terminal:"
	@echo ""
	@echo "  Terminal 1 (backend):  make server-dev"
	@echo "  Terminal 2 (frontend): make frontend-dev"
	@echo ""
	@echo "Both processes write to stdout in their own terminal."
	@echo "Prerequisites: Go, Node.js, PostgreSQL (run 'make setup' first)."

# Run only the Next.js frontend, proxying API calls to a remote backend.
# Useful on low-resource machines or when you only need to work on the UI.
# Usage: REMOTE_API_URL=https://staging.example.com make dev-frontend-only
dev-frontend-only:
	@if [ -z "$(REMOTE_API_URL)" ]; then \
		echo "Error: REMOTE_API_URL is required."; \
		echo ""; \
		echo "Usage: REMOTE_API_URL=https://your-backend.example.com make dev-frontend-only"; \
		exit 1; \
	fi
	cd frontend && API_PROXY_TARGET=$(REMOTE_API_URL) npm run dev

setup:
	./setup.sh

test:
	go test ./...

test-race:
	go test -race ./...

test-coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Mirror the PR backend job: no -race, coverage on, no absolute floor.
test-pr:
	go test ./internal/... -coverprofile=coverage.out -covermode=count -timeout=120s

# Patch coverage gate vs origin/main (mirrors the PR diff-cover step).
# Requires: pip install 'diff_cover==10.2.0' && go install github.com/boumenot/gocover-cobertura@v1.4.0
test-coverage-diff: test-pr
	gocover-cobertura < coverage.out > coverage.xml
	diff-cover coverage.xml --compare-branch=origin/main --fail-under=80

# Mirror the merge-to-main backend job: race detector on + absolute floor check.
test-main:
	go test ./internal/... -coverprofile=coverage.out -covermode=atomic -race -timeout=180s
	go tool cover -func=coverage.out

migrate-up:
	go run cmd/migrate/main.go up

migrate-down:
	go run cmd/migrate/main.go down

BUILD_SHA ?= $(shell git rev-parse HEAD 2>/dev/null || echo dev)
LDFLAGS := -X github.com/assembledhq/143/internal/version.BuildSHA=$(BUILD_SHA)

build:
	go build -ldflags "$(LDFLAGS)" -o bin/server ./cmd/server
	go build -o bin/migrate ./cmd/migrate

frontend-dev:
	cd frontend && npm run dev

frontend-lint:
	cd frontend && npm run lint

frontend-typecheck:
	cd frontend && npm run typecheck

frontend-check:
	cd frontend && npm run typecheck && npm run lint && npm run build

server-dev:
	go run -ldflags "$(LDFLAGS)" cmd/server/main.go

lint: lint-tenancy
	@$(MAKE) lint-bootstrap
	$(GOLANGCI_LINT_BIN) run ./...

# Multi-tenancy guardrails — see AGENTS.md ("Multi-tenancy").
# Schema: every new table must declare org_id (or be allowlisted in cmd/lint-schema).
# Stores: every exported *Store method must take org scope (or be annotated
#         with // lint:allow-no-orgid reason="...").
lint-tenancy: lint-schema lint-stores

lint-schema:
	@go run ./cmd/lint-schema

lint-stores:
	@go run ./cmd/lint-stores

lint-bootstrap:
	@mkdir -p $(CURDIR)/bin
	GOTOOLCHAIN=$(GO_TOOLCHAIN_VERSION) GOBIN=$(CURDIR)/bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

# Point git at .githooks/. The pre-commit hook runs lint-schema / lint-stores
# (scoped to the staged files) and gofmt before every commit. Skip with
# `git commit --no-verify` or disable with `make hooks-uninstall`.
#
# Refuses to clobber a pre-existing core.hooksPath pointing elsewhere (a
# monorepo parent, a custom dev setup) — override intentionally by running
# `git config core.hooksPath .githooks` by hand.
hooks-install:
	@git rev-parse --git-dir >/dev/null 2>&1 || { echo "Not a git repo"; exit 1; }
	@existing=$$(git config --get core.hooksPath 2>/dev/null || true); \
	if [ -z "$$existing" ] || [ "$$existing" = ".githooks" ]; then \
	  git config core.hooksPath .githooks; \
	  echo "Installed: git will now run .githooks/pre-commit on every commit."; \
	else \
	  echo "ERROR: core.hooksPath is already set to '$$existing'." >&2; \
	  echo "Refusing to overwrite. To use 143's hooks anyway, run:" >&2; \
	  echo "  git config core.hooksPath .githooks" >&2; \
	  exit 1; \
	fi

hooks-uninstall:
	@existing=$$(git config --get core.hooksPath 2>/dev/null || true); \
	if [ "$$existing" = ".githooks" ]; then \
	  git config --unset core.hooksPath; \
	  echo "Uninstalled: git will use the default .git/hooks/ path."; \
	else \
	  echo "core.hooksPath is '$$existing' (not .githooks) — leaving alone."; \
	fi

# ── Secrets management (SOPS + age) ─────────────────────────────────
# Optional — only needed if you want encrypted secrets committed to git.
# Most contributors just use .env directly (see .env.example).
#
# Prerequisites: brew install sops age  (or apt install sops age)
#
# Quick start:
#   1. make secrets-setup        — generate an age keypair (one-time)
#   2. Paste your public key into .sops.yaml
#   3. Fill in .env with real values
#   4. make secrets-encrypt      — encrypt .env → .env.enc
#
# Per-environment usage (ENV defaults to empty = development):
#   make secrets-encrypt ENV=staging    — .env.staging → .env.staging.enc
#   make secrets-decrypt ENV=staging    — .env.staging.enc → .env.staging
#   make secrets-edit    ENV=staging    — edit .env.staging.enc in-place
#
# See docs/secrets/README.md for the full guide.

export SOPS_AGE_KEY_FILE ?= $(HOME)/.config/sops/age/keys.txt
ENV ?=
_ENV_LC := $(shell echo '$(ENV)' | tr '[:upper:]' '[:lower:]')

# Resolve file names from ENV. "" → .env / .env.enc, "staging" → .env.staging / .env.staging.enc
ifdef ENV
  _ENV_FILE     := .env.$(_ENV_LC)
  _ENV_ENC_FILE := .env.$(_ENV_LC).enc
else
  _ENV_FILE     := .env
  _ENV_ENC_FILE := .env.enc
endif

secrets-setup:
	@command -v age-keygen >/dev/null 2>&1 || { echo "Install age: brew install age  (or: apt install age)"; exit 1; }
	@command -v sops >/dev/null 2>&1 || { echo "Install sops: brew install sops  (or: apt install sops)"; exit 1; }
	@if [ -f $(SOPS_AGE_KEY_FILE) ]; then \
		echo "age key already exists at $(SOPS_AGE_KEY_FILE)"; \
		echo "Public key:"; grep "public key:" $(SOPS_AGE_KEY_FILE) | awk '{print $$4}'; \
	else \
		mkdir -p $$(dirname $(SOPS_AGE_KEY_FILE)); \
		age-keygen -o $(SOPS_AGE_KEY_FILE) 2>&1; \
		echo ""; \
		echo "Key saved to $(SOPS_AGE_KEY_FILE)"; \
		echo ""; \
		echo "Next steps:"; \
		echo "  1. Copy the public key printed above"; \
		echo "  2. Paste it into .sops.yaml (replace the TODO placeholder)"; \
		echo "  3. Run: make secrets-encrypt"; \
	fi

secrets-encrypt:
	@test -f $(_ENV_FILE) || { echo "No $(_ENV_FILE) to encrypt. Copy .env.example to $(_ENV_FILE) first."; exit 1; }
	@test -f .sops.yaml || { echo "No .sops.yaml found. Run make secrets-setup first."; exit 1; }
	sops --encrypt --input-type dotenv --output-type dotenv $(_ENV_FILE) > $(_ENV_ENC_FILE)
	@echo "Encrypted $(_ENV_FILE) → $(_ENV_ENC_FILE) (safe to commit)"

secrets-decrypt:
	@test -f $(_ENV_ENC_FILE) || { echo "No $(_ENV_ENC_FILE) found."; exit 1; }
	sops --decrypt --input-type dotenv --output-type dotenv $(_ENV_ENC_FILE) > $(_ENV_FILE)
	@echo "Decrypted $(_ENV_ENC_FILE) → $(_ENV_FILE)"

secrets-edit:
	@test -f $(_ENV_ENC_FILE) || { echo "No $(_ENV_ENC_FILE) found."; exit 1; }
	sops --input-type dotenv --output-type dotenv $(_ENV_ENC_FILE)

# Re-encrypt all .enc files with the current .sops.yaml keys.
# Run this after adding a new team member's public key to .sops.yaml.
secrets-rotate:
	@command -v sops >/dev/null 2>&1 || { echo "Install sops: brew install sops"; exit 1; }
	@for f in .env*.enc; do \
		[ -f "$$f" ] || continue; \
		echo "Rotating keys for $$f ..."; \
		sops updatekeys --yes --input-type dotenv "$$f"; \
	done
	@echo "Done. Commit the updated .enc files."

# ── Multi-node provisioning & deployment ─────────────────────────────
# Provision a fresh node (installs Docker, gVisor, copies configs, starts services).
# Reads age key from ~/.config/sops/age/keys.txt and all other secrets
# (DB_PASSWORD, DB_HOST, GHCR_TOKEN) from .env.production.enc automatically.
#
# SSH_KEY is auto-detected from ~/.ssh/143-deploy but can be overridden.
# Usage:
#   make provision-app    HOST=87.99.150.138
#   make provision-worker HOST=87.99.158.39
#   make provision-db     HOST=87.99.157.55
#
# To tear down and reprovision an existing node:
#   make provision-app    HOST=87.99.150.138  REPROVISION=true

REPROVISION ?=

# Auto-detect SSH key: use ~/.ssh/143-deploy if it exists.
SSH_KEY ?= $(wildcard ~/.ssh/143-deploy)

# Guard: fail with a helpful message when SSH_KEY is empty.
define check-ssh-key
@test -n "$(SSH_KEY)" || { echo "SSH_KEY could not be auto-detected. Set SSH_KEY=<path>."; exit 1; }
endef

provision-app:
	@test -n "$(HOST)" || { echo "HOST is required. Usage: make provision-app HOST=<ip> [SSH_KEY=<path>]"; exit 1; }
	$(check-ssh-key)
	./deploy/scripts/provision.sh app $(HOST) $(SSH_KEY) $(if $(REPROVISION),--reprovision)

provision-worker:
	@test -n "$(HOST)" || { echo "HOST is required. Usage: make provision-worker HOST=<ip> [SSH_KEY=<path>]"; exit 1; }
	$(check-ssh-key)
	./deploy/scripts/provision.sh worker $(HOST) $(SSH_KEY) $(if $(REPROVISION),--reprovision)

provision-db:
	@test -n "$(HOST)" || { echo "HOST is required. Usage: make provision-db HOST=<ip> [SSH_KEY=<path>]"; exit 1; }
	$(check-ssh-key)
	./deploy/scripts/provision.sh db $(HOST) $(SSH_KEY) $(if $(REPROVISION),--reprovision)

provision-logging:
	@test -n "$(HOST)" || { echo "HOST is required. Usage: make provision-logging HOST=<ip> [SSH_KEY=<path>]"; exit 1; }
	$(check-ssh-key)
	./deploy/scripts/provision.sh logging $(HOST) $(SSH_KEY) $(if $(REPROVISION),--reprovision)

# Deploy (update) an already-provisioned node.
# HOST is optional — falls back to the matching role in FLEET_HOSTS from .env.production.enc.
# SSH_KEY is auto-detected from ~/.ssh/143-deploy but can be overridden.
# Usage:
#   make deploy-app
#   make deploy-app    HOST=87.99.150.138

# Shell snippet to read FLEET_HOSTS from env var or .env.production.enc via SOPS.
# Sets $$FLEET. Use inside a recipe with: $(read-fleet-hosts);
define read-fleet-hosts
FLEET="$${FLEET_HOSTS:-}"; \
if [ -z "$$FLEET" ]; then \
	FLEET="$$(sops --decrypt --input-type dotenv --output-type dotenv .env.production.enc 2>/dev/null | grep '^FLEET_HOSTS=' | cut -d= -f2- || true)"; \
fi
endef

# Resolve HOST(s) from FLEET_HOSTS for a given role and deploy each.
# If HOST is set explicitly, deploys to that single host.
# Otherwise, deploys to ALL hosts matching the role in FLEET_HOSTS.
# Usage: @$(call resolve-host,role)
define resolve-host
if [ -n "$(HOST)" ]; then \
	echo "Deploying $(1) → $(HOST)"; \
	./deploy/scripts/deploy.sh $(1) $(HOST) $(SSH_KEY); \
else \
	$(read-fleet-hosts); \
	HOSTS="$$(echo "$$FLEET" | tr ',' '\n' | grep '^$(1):' | cut -d: -f2)"; \
	if [ -z "$$HOSTS" ]; then \
		echo "ERROR: Could not resolve host for role '$(1)'. Set HOST or add $(1):<ip> to FLEET_HOSTS."; \
		exit 1; \
	fi; \
	for h in $$HOSTS; do \
		echo "Deploying $(1) → $$h"; \
		./deploy/scripts/deploy.sh $(1) $$h $(SSH_KEY); \
	done; \
fi
endef

deploy-app:
	$(check-ssh-key)
	@$(call resolve-host,app)

deploy-worker:
	$(check-ssh-key)
	@$(call resolve-host,worker)

deploy-db:
	$(check-ssh-key)
	@$(call resolve-host,db)

deploy-logging:
	$(check-ssh-key)
	@$(call resolve-host,logging)

# Deploy all nodes in the fleet.
# Uses FLEET_HOSTS env var or FLEET_HOSTS in .env.production.enc.
deploy-fleet:
	$(check-ssh-key)
	./deploy/scripts/deploy-fleet.sh $(SSH_KEY)

# Shorthand alias for deploy-fleet.
deploy: deploy-fleet

# Sync SSH public keys from deploy/authorized_keys/*.pub to all fleet nodes.
# Dry-run by default — shows diff without changing anything.
# Usage: make sync-keys              (dry run)
#        make sync-keys APPLY=true   (actually push changes)
APPLY ?=
sync-keys:
	$(check-ssh-key)
	@$(read-fleet-hosts); \
	HOSTS="$$(echo "$$FLEET" | tr ',' '\n' | cut -d: -f2 | sort -u)"; \
	if [ -z "$$HOSTS" ]; then \
		echo "ERROR: No hosts found. Set FLEET_HOSTS or add entries to .env.production.enc."; \
		exit 1; \
	fi; \
	./deploy/scripts/sync-keys.sh $(if $(APPLY),--apply) $(SSH_KEY) $$HOSTS

# Open Grafana via SSH tunnel.
# Usage: make logs [SSH_KEY=~/.ssh/143-deploy]
logs:
	$(check-ssh-key)
	@LOGGING_HOST="$(LOGGING_HOST)"; \
	if [ -z "$$LOGGING_HOST" ]; then \
		$(read-fleet-hosts); \
		LOGGING_HOST="$$(echo "$$FLEET" | tr ',' '\n' | grep '^logging:' | cut -d: -f2 | head -1)"; \
	fi; \
	if [ -z "$$LOGGING_HOST" ]; then \
		echo "ERROR: Could not find logging host. Set LOGGING_HOST or add logging:<ip> to FLEET_HOSTS."; \
		exit 1; \
	fi; \
	echo "Opening Grafana tunnel → http://localhost:9999"; \
	echo "Press Ctrl+C to close."; \
	ssh -i $(SSH_KEY) -L 9999:localhost:9999 -N deploy@$$LOGGING_HOST
