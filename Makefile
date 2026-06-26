# Stamp file tracking when the sandbox image was last built.
SANDBOX_STAMP := sandbox/.build-stamp
SANDBOX_SOURCES := sandbox/Dockerfile sandbox/versions.json

.PHONY: dev dev-ngrok dev-local dev-frontend-only setup test test-race test-coverage test-pr test-coverage-diff test-main test-integration migrate-up migrate-down demo-seed-check demo-seed-apply build build-cli frontend-dev frontend-lint frontend-typecheck frontend-check lint lint-bootstrap lint-schema lint-stores lint-tenancy hooks-install hooks-uninstall secrets-setup secrets-encrypt secrets-decrypt secrets-edit secrets-rotate single-node-prepare single-node-up single-node-down provision-app provision-worker provision-workers provision-egress provision-db provision-logging provision-redis tailscale-enroll repair-deploy-sudoers repair-worker-host spin-down-worker deploy deploy-app deploy-worker deploy-worker-preflight deploy-db deploy-logging deploy-fleet logs logs-query setup-readonly-user db-psql db-query

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

# Integration tests against a real Postgres. Gated behind the `integration`
# build tag so `make test` and `go test ./...` stay fast. Reuses DATABASE_URL
# (the same env CI's backend-test job already exports), so locally:
#   docker compose up -d postgres && make migrate-up && make test-integration
test-integration:
	INTEGRATION_DATABASE_URL=$${INTEGRATION_DATABASE_URL:-$$DATABASE_URL} \
	go test -tags=integration -timeout=120s ./internal/integration/...

migrate-up:
	go run cmd/migrate/main.go up

migrate-down:
	go run cmd/migrate/main.go down

# Validates the public/demo seed without mutating the source database.
# Creates a temporary sibling database on the configured Postgres server,
# runs migrations, applies .143/seed twice, asserts required demo rows, and
# drops the temporary database. Override the admin connection with
# DEMO_SEED_CHECK_DATABASE_URL or `make demo-seed-check DATABASE_URL=...`.
demo-seed-check:
	go run ./cmd/demo-seed check

# Applies the canonical public/demo seed to an explicit demo database.
# Guarded intentionally: set all three env vars below so this cannot
# accidentally run against production or a personal dev DB.
# Usage:
#   DEMO_MODE=true ALLOW_DEMO_SEED_APPLY=true \
#   DEMO_SEED_DATABASE_URL=postgres://... make demo-seed-apply
demo-seed-apply:
	@test -n "$$DEMO_SEED_DATABASE_URL" || { echo "DEMO_SEED_DATABASE_URL is required."; exit 1; }
	@test "$$DEMO_MODE" = "true" || { echo "DEMO_MODE=true is required."; exit 1; }
	@test "$$ALLOW_DEMO_SEED_APPLY" = "true" || { echo "ALLOW_DEMO_SEED_APPLY=true is required."; exit 1; }
	go run ./cmd/demo-seed apply

BUILD_SHA ?= $(shell git rev-parse HEAD 2>/dev/null || echo dev)
LDFLAGS := -X github.com/assembledhq/143/internal/version.BuildSHA=$(BUILD_SHA)

build:
	go build -ldflags "$(LDFLAGS)" -o bin/server ./cmd/server
	go build -o bin/migrate ./cmd/migrate

# Cross-compile the 143-tools CLI for laptop installs. Outputs to dist/cli/
# plus a checksums.txt the installer script verifies against. The server
# image bakes this directory in at /opt/143/cli (see Dockerfile) and serves
# it from /download/143-tools/*. The platform matrix must stay in sync with
# cliPlatforms in internal/api/handlers/cli_distribution.go.
CLI_DIST_DIR := dist/cli
CLI_PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64

build-cli:
	rm -rf $(CLI_DIST_DIR)
	mkdir -p $(CLI_DIST_DIR)
	@set -e; for platform in $(CLI_PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		echo "building $(CLI_DIST_DIR)/143-tools-$$os-$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch \
			go build -trimpath -ldflags "$(LDFLAGS)" \
			-o $(CLI_DIST_DIR)/143-tools-$$os-$$arch ./cmd/tools; \
	done
	@cd $(CLI_DIST_DIR) && if command -v sha256sum >/dev/null 2>&1; then \
		sha256sum 143-tools-* > checksums.txt; \
	else \
		shasum -a 256 143-tools-* > checksums.txt; \
	fi
	@echo "wrote $(CLI_DIST_DIR)/checksums.txt"

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

single-node-prepare:
	sudo ./deploy/scripts/prepare-single-node.sh

single-node-up:
	@test -f .env.single-node || { echo "Create .env.single-node first: cp .env.single-node.example .env.single-node"; exit 1; }
	docker compose --env-file .env.single-node -f docker-compose.single-node.yml up -d

single-node-down:
	docker compose --env-file .env.single-node -f docker-compose.single-node.yml down

# ── Secrets management (SOPS + age) ─────────────────────────────────
# Optional — only needed if you want encrypted secrets kept in a private
# repo. Most contributors just use .env directly (see .env.example).
#
# Encrypted bundles (.env*.enc) and .sops.yaml live OUTSIDE this public
# repo, in a private sibling checkout (default: ../143-infra). Override
# with SECRETS_DIR=/path/to/checkout. Plaintext working copies (.env,
# .env.production) stay at the repo root and are gitignored.
#
# Prerequisites: brew install sops age  (or apt install sops age)
#
# Quick start:
#   1. make secrets-setup        — generate an age keypair (one-time)
#   2. Paste your public key into $(SECRETS_DIR)/.sops.yaml
#   3. Fill in .env with real values
#   4. make secrets-encrypt      — encrypt .env → $(SECRETS_DIR)/.env.enc
#
# Per-environment usage (ENV defaults to empty = development):
#   make secrets-encrypt ENV=staging    — .env.staging → $(SECRETS_DIR)/.env.staging.enc
#   make secrets-decrypt ENV=staging    — $(SECRETS_DIR)/.env.staging.enc → .env.staging
#   make secrets-edit    ENV=staging    — edit $(SECRETS_DIR)/.env.staging.enc in-place
#
# See docs/secrets/README.md for the full guide, including how to
# bootstrap the private repo.

export SOPS_AGE_KEY_FILE ?= $(HOME)/.config/sops/age/keys.txt
# Default SECRETS_DIR to a sibling of the MAIN checkout, resolved
# worktree-safely by the same helper the deploy scripts use (linked
# worktrees — Claude Code, Codex, Conductor — share the main repo's .git).
# Falls back to a plain sibling path if the helper can't run.
_SECRETS_DIR_DEFAULT := $(shell ./deploy/scripts/resolve-secrets-dir.sh . 2>/dev/null)
export SECRETS_DIR ?= $(or $(_SECRETS_DIR_DEFAULT),../143-infra)
_SOPS_CONFIG := $(SECRETS_DIR)/.sops.yaml
_PROD_ENC := $(SECRETS_DIR)/.env.production.enc
ENV ?=
_ENV_LC := $(shell echo '$(ENV)' | tr '[:upper:]' '[:lower:]')

# Resolve file names from ENV. "" → .env / .env.enc, "staging" → .env.staging / .env.staging.enc
ifdef ENV
  _ENV_FILE     := .env.$(_ENV_LC)
  _ENV_ENC_FILE := $(SECRETS_DIR)/.env.$(_ENV_LC).enc
else
  _ENV_FILE     := .env
  _ENV_ENC_FILE := $(SECRETS_DIR)/.env.enc
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
		echo "  2. Paste it into $(_SOPS_CONFIG) (replace the TODO placeholder)"; \
		echo "  3. Run: make secrets-encrypt"; \
	fi

secrets-encrypt:
	@test -f $(_ENV_FILE) || { echo "No $(_ENV_FILE) to encrypt. Copy .env.example to $(_ENV_FILE) first."; exit 1; }
	@test -f $(_SOPS_CONFIG) || { echo "No $(_SOPS_CONFIG) found. Clone the private secrets repo next to this one (see docs/secrets/README.md) or set SECRETS_DIR."; exit 1; }
	sops --encrypt --config $(_SOPS_CONFIG) --input-type dotenv --output-type dotenv $(_ENV_FILE) > $(_ENV_ENC_FILE)
	@echo "Encrypted $(_ENV_FILE) → $(_ENV_ENC_FILE) (commit it in $(SECRETS_DIR))"

secrets-decrypt:
	@test -f $(_ENV_ENC_FILE) || { echo "No $(_ENV_ENC_FILE) found. Clone the private secrets repo next to this one or set SECRETS_DIR."; exit 1; }
	sops --decrypt --input-type dotenv --output-type dotenv $(_ENV_ENC_FILE) > $(_ENV_FILE)
	@echo "Decrypted $(_ENV_ENC_FILE) → $(_ENV_FILE)"

secrets-edit:
	@test -f $(_ENV_ENC_FILE) || { echo "No $(_ENV_ENC_FILE) found. Clone the private secrets repo next to this one or set SECRETS_DIR."; exit 1; }
	sops --input-type dotenv --output-type dotenv $(_ENV_ENC_FILE)

# Re-encrypt all .enc files with the current $(SECRETS_DIR)/.sops.yaml keys.
# Run this after adding a new team member's public key to .sops.yaml.
secrets-rotate:
	@command -v sops >/dev/null 2>&1 || { echo "Install sops: brew install sops"; exit 1; }
	@for f in $(SECRETS_DIR)/.env*.enc; do \
		[ -f "$$f" ] || continue; \
		echo "Rotating keys for $$f ..."; \
		sops --config $(_SOPS_CONFIG) updatekeys --yes --input-type dotenv "$$f" || exit 1; \
	done
	@echo "Done. Commit the updated .enc files in $(SECRETS_DIR)."

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
#   make provision-redis  HOST=10.0.0.50
#
# DB-only env vars:
#   DB_BIND_IP                   — required for db role. Set to the db node's
#                                  primary private IP so same-datacenter nodes
#                                  keep a DB path if Tailscale is unavailable.
#                                  DB Tailscale enrollment advertises DB_BIND_IP/32
#                                  automatically when TS_AUTH_KEY_DB is present.
#
# Worker-only env vars (per-host identity, written to /opt/143/.env.local
# and preserved across deploys):
#   WORKER_PRIVATE_IP            — auto-detected via SSH if unset. Multi-homed
#                                  hosts (cluster NIC + storage VLAN, etc.)
#                                  abort with the candidate list so you can
#                                  pick the one app nodes will reach.
#   NODE_ID                      — defaults to "worker-<WORKER_PRIVATE_IP with
#                                  dots replaced by dashes>" (e.g. worker-10-0-0-4),
#                                  unique across the full RFC1918 space.
#   PREVIEW_INTERNAL_BASE_URL    — defaults to "http://${WORKER_PRIVATE_IP}:8080"
#
# Optional Tailscale provisioning env vars:
#   TS_AUTH_KEY_<ROLE>           — role-specific auth keys: TS_AUTH_KEY_APP,
#                                  TS_AUTH_KEY_DB, TS_AUTH_KEY_WORKER,
#                                  TS_AUTH_KEY_REDIS, TS_AUTH_KEY_EGRESS.
#   TS_TAG_<ROLE>                — role-specific tags. Defaults to tag:prod-<role>.
#   TS_HOSTNAME                  — defaults to 143-<role>-<HOST with dots as dashes>.
#   TS_WORKER_HOSTS              — comma-separated tailnet workers. Entries can be
#                                  "<host>" or "<node-id>:<host>". Mapped
#                                  workers always accept advertised routes.
#
# Example with overrides:
#   make provision-worker HOST=87.99.158.39 WORKER_PRIVATE_IP=10.0.0.4 NODE_ID=worker-1
#   make provision-app HOST=<public-ip>
#   make provision-db HOST=<public-ip>
#   make provision-worker HOST=<public-ip>
#   make tailscale-enroll ROLE=app HOST=<existing-app-public-ip>
#   make tailscale-enroll ROLE=redis HOST=<existing-redis-public-ip>
#
# Static egress worker provisioning:
#   When STATIC_EGRESS_PUBLIC_IP is configured, provision-worker derives
#   WireGuard peer config from worker:<host> entries in FLEET_HOSTS, updates
#   generated static egress fields in .env.production.enc, reloads the egress
#   gateway from the egress:<host> FLEET_HOSTS entry, then provisions the
#   worker. Add worker:<HOST> to FLEET_HOSTS before running make
#   provision-worker HOST=<HOST>. EGRESS_SSH_KEY defaults to
#   ~/.ssh/143-egress or ~/.ssh/143-egress.pem when present, then falls back
#   to SSH_KEY. The gateway SSH user is auto-detected as root or ubuntu; set
#   EGRESS_SSH_USER only for unusual images.
#
# To tear down and reprovision an existing node:
#   make provision-app    HOST=87.99.150.138  REPROVISION=true
#
# Migration note for fleets provisioned before /opt/143/.env.local existed:
# `make deploy-worker` against such a host fails loudly at the secrets step
# with "ERROR: /opt/143/.env.local is missing on this host." To recover,
# either run `make provision-worker HOST=<host> REPROVISION=true` (tears
# down the worker container — drains active jobs first) or seed .env.local
# manually:
#   ssh deploy@<host> 'cat > /opt/143/.env.local <<EOF
#   NODE_ID=worker-N
#   WORKER_PRIVATE_IP=10.0.0.N
#   PREVIEW_INTERNAL_BASE_URL=http://10.0.0.N:8080
#   EOF
#   chmod 600 /opt/143/.env.local'
#
# Reprovisioning leaves the old `nodes` row behind. nodes.id stores NODE_ID,
# so a host that was provisioned as e.g. "worker-4" and later reprovisioned
# under the new dotted-to-dash default ("worker-10-0-0-4") registers a fresh
# row instead of updating the old one. The MarkStaleNodesDead reaper flips
# the orphan to status='dead' once heartbeats stop, and ListActive filters
# 'active'/'draining' only — so the orphan does NOT route preview traffic.
# It just sits in the table until cleaned up. To delete it explicitly:
#   psql "$DATABASE_URL" -c "DELETE FROM nodes WHERE id = 'worker-4' AND status = 'dead';"
# Preserve old NODE_IDs across reprovision instead by passing the old value:
#   make provision-worker HOST=<host> REPROVISION=true NODE_ID=worker-4

REPROVISION ?=

# Per-host worker identity (forwarded as env vars to provision.sh / deploy.sh
# so users can write `make provision-worker HOST=… WORKER_PRIVATE_IP=…` instead
# of prefixing the env var on the command line). Empty by default; provision.sh
# auto-detects WORKER_PRIVATE_IP and DOCKER_GID via SSH when unset and derives
# NODE_ID and PREVIEW_INTERNAL_BASE_URL from it.
export WORKER_PRIVATE_IP
export WORKER_PRIVATE_IP_SOURCE
export NODE_ID
export PREVIEW_INTERNAL_BASE_URL
export DOCKER_GID
export DB_BIND_IP
export TS_AUTH_KEY_APP
export TS_AUTH_KEY_DB
export TS_AUTH_KEY_WORKER
export TS_AUTH_KEY_REDIS
export TS_AUTH_KEY_EGRESS
export TS_TAG_APP
export TS_TAG_DB
export TS_TAG_WORKER
export TS_TAG_REDIS
export TS_TAG_EGRESS
export TS_WORKER_HOSTS
export TS_AUTH_KEY
export TS_TAG
export TS_HOSTNAME
export TS_ADVERTISE_ROUTES
export EGRESS_SSH_USER
export SSH_USER

# Auto-detect SSH key: use ~/.ssh/143-deploy if it exists.
SSH_KEY ?= $(wildcard ~/.ssh/143-deploy)
EGRESS_SSH_KEY ?= $(or $(wildcard ~/.ssh/143-egress),$(wildcard ~/.ssh/143-egress.pem),$(SSH_KEY))

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
	@test -n "$(EGRESS_SSH_KEY)" || { echo "EGRESS_SSH_KEY could not be auto-detected. Put the gateway key at ~/.ssh/143-egress or ~/.ssh/143-egress.pem, or set EGRESS_SSH_KEY=<path> or SSH_KEY=<path>."; exit 1; }
	@PROVISION_WORKER_HOST=$(HOST) deploy/scripts/sync-static-egress-secrets.sh --apply
	@deploy/scripts/provision-egress.sh "" "$(EGRESS_SSH_KEY)"
	./deploy/scripts/provision.sh worker $(HOST) $(SSH_KEY) $(if $(REPROVISION),--reprovision)

provision-egress:
	@test -n "$(EGRESS_SSH_KEY)" || { echo "EGRESS_SSH_KEY could not be auto-detected. Put the gateway key at ~/.ssh/143-egress or ~/.ssh/143-egress.pem, or set EGRESS_SSH_KEY=<path> or SSH_KEY=<path>."; exit 1; }
	@deploy/scripts/sync-static-egress-secrets.sh --apply
	@deploy/scripts/provision-egress.sh "$(HOST)" "$(EGRESS_SSH_KEY)"

# Provision (or re-provision) every worker:<host> in FLEET_HOSTS in one pass.
# Syncs per-worker WireGuard secrets and provisions the egress gateway once,
# then runs provision.sh for each worker host. Use after enabling/repairing
# static egress so every worker rewrites /etc/143/static-egress-capable and
# starts advertising static_egress_capable in its node metadata.
#
# On an already-running fleet you must pass REPROVISION=true: provision.sh
# aborts when services are already running, so plain mode only works for
# fresh hosts. REPROVISION=true tears down and rebuilds each worker (drains
# active jobs first). Reprovisioning can orphan old `nodes` rows when
# NODE_IDs change — see the reprovision notes above provision-worker.
#
# Workers are provisioned concurrently, PROVISION_JOBS at a time (default 4),
# with per-host output written to log files under /tmp. Note that with
# REPROVISION=true and PROVISION_JOBS>1, several workers can be down at the
# same time — set PROVISION_JOBS=1 to keep the old one-worker-down-at-a-time
# behavior. A failed host doesn't stop the others; the target exits nonzero
# at the end. Rerun to resume (secrets sync and gateway provisioning are
# idempotent).
# Usage:
#   make provision-workers
#   make provision-workers REPROVISION=true
#   make provision-workers PROVISION_JOBS=8
#   make provision-workers EGRESS_SSH_USER=admin EGRESS_SSH_KEY=~/.ssh/custom-egress-key
PROVISION_JOBS ?= 4
provision-workers:
	$(check-ssh-key)
	@test -n "$(EGRESS_SSH_KEY)" || { echo "EGRESS_SSH_KEY could not be auto-detected. Put the gateway key at ~/.ssh/143-egress or ~/.ssh/143-egress.pem, or set EGRESS_SSH_KEY=<path> or SSH_KEY=<path>."; exit 1; }
	@deploy/scripts/sync-static-egress-secrets.sh --apply
	@deploy/scripts/provision-egress.sh "" "$(EGRESS_SSH_KEY)"
	@$(read-fleet-hosts); \
	HOSTS="$$(echo "$$FLEET" | tr ',' '\n' | grep '^worker:' | cut -d: -f2)"; \
	if [ -z "$$HOSTS" ]; then \
		echo "ERROR: no worker:<host> entries in FLEET_HOSTS."; exit 1; \
	fi; \
	LOG_DIR="$$(mktemp -d /tmp/provision-workers.XXXXXX)"; \
	echo "=== provisioning $$(echo "$$HOSTS" | wc -l | tr -d ' ') workers, $(PROVISION_JOBS) at a time (logs: $$LOG_DIR) ==="; \
	if echo "$$HOSTS" | LOG_DIR="$$LOG_DIR" xargs -n1 -P "$(PROVISION_JOBS)" sh -c '\
		h="$$1"; log="$$LOG_DIR/$$h.log"; \
		echo "--- provisioning worker $$h (log: $$log)"; \
		if ./deploy/scripts/provision.sh worker "$$h" $(SSH_KEY) $(if $(REPROVISION),--reprovision) >"$$log" 2>&1; then \
			echo "--- OK: $$h"; \
		else \
			echo "--- FAILED: $$h (log: $$log)"; exit 1; \
		fi' provision-one; then \
		echo "=== all workers provisioned ==="; \
	else \
		echo "=== FAILED: one or more workers failed; see logs in $$LOG_DIR ==="; exit 1; \
	fi

provision-db:
	@test -n "$(HOST)" || { echo "HOST is required. Usage: make provision-db HOST=<ip> [SSH_KEY=<path>]"; exit 1; }
	$(check-ssh-key)
	./deploy/scripts/provision.sh db $(HOST) $(SSH_KEY) $(if $(REPROVISION),--reprovision)

provision-logging:
	@test -n "$(HOST)" || { echo "HOST is required. Usage: make provision-logging HOST=<ip> [SSH_KEY=<path>]"; exit 1; }
	$(check-ssh-key)
	./deploy/scripts/provision.sh logging $(HOST) $(SSH_KEY) $(if $(REPROVISION),--reprovision)

provision-redis:
	@test -n "$(HOST)" || { echo "HOST is required. Usage: make provision-redis HOST=<ip> [SSH_KEY=<path>]"; exit 1; }
	$(check-ssh-key)
	./deploy/scripts/provision.sh redis $(HOST) $(SSH_KEY) $(if $(REPROVISION),--reprovision)

tailscale-enroll:
	@test -n "$(ROLE)" || { echo "ROLE is required. Usage: make tailscale-enroll ROLE=<app|db|redis> HOST=<ip> [SSH_KEY=<path>]"; exit 1; }
	@test "$(ROLE)" = "app" -o "$(ROLE)" = "db" -o "$(ROLE)" = "redis" || { echo "ROLE must be app, db, or redis. Use provision-worker for new tailnet workers."; exit 1; }
	@test -n "$(HOST)" || { echo "HOST is required. Usage: make tailscale-enroll ROLE=<app|db|redis> HOST=<ip> [SSH_KEY=<path>]"; exit 1; }
	$(check-ssh-key)
	./deploy/scripts/provision.sh $(ROLE) $(HOST) $(SSH_KEY) --tailscale-only

# Refresh deploy's narrow sudoers grant on an existing host without tearing
# down containers. Use when deploy fails on a legacy host with
# "sudo: a password is required" from a deploy-time helper.
# Usage:
#   make repair-deploy-sudoers ROLE=app HOST=87.99.150.138
repair-deploy-sudoers:
	@test -n "$(ROLE)" || { echo "ROLE is required. Usage: make repair-deploy-sudoers ROLE=<app|worker|db|logging|redis> HOST=<ip> [SSH_KEY=<path>]"; exit 1; }
	@test -n "$(HOST)" || { echo "HOST is required. Usage: make repair-deploy-sudoers ROLE=<app|worker|db|logging|redis> HOST=<ip> [SSH_KEY=<path>]"; exit 1; }
	$(check-ssh-key)
	bash ./deploy/scripts/repair-deploy-sudoers.sh $(ROLE) $(HOST) $(SSH_KEY)

# Re-apply canonical worker host invariants without tearing down containers.
# Repairs sandbox network/firewall/resolv.conf, sandbox-auth socket dir, and
# worker sysctl drift. Requires the host to already have /opt/143/deploy staged.
# Usage:
#   make repair-worker-host HOST=87.99.158.39
repair-worker-host:
	@test -n "$(HOST)" || { echo "HOST is required. Usage: make repair-worker-host HOST=<ip> [SSH_KEY=<path>]"; exit 1; }
	$(check-ssh-key)
	ssh -i $(SSH_KEY) -o BatchMode=yes -o StrictHostKeyChecking=accept-new deploy@$(HOST) 'sudo -n /opt/143/deploy/scripts/reconcile-worker-host.sh 143-sandbox'

# Drain a worker host, stop its worker compose services, and optionally clear
# worker-owned Docker state. Set CLEAR=true only when intentionally emptying
# the machine for decommission/reprovision.
# Usage:
#   make spin-down-worker HOST=87.99.158.39
#   make spin-down-worker HOST=87.99.158.39 CLEAR=true
#   make spin-down-worker HOST=87.99.158.39 TIMEOUT=7200 EXECUTOR_TIMEOUT=600
spin-down-worker:
	@test -n "$(HOST)" || { echo "HOST is required. Usage: make spin-down-worker HOST=<ip> [SSH_KEY=<path>] [CLEAR=true] [TIMEOUT=seconds] [EXECUTOR_TIMEOUT=seconds]"; exit 1; }
	$(check-ssh-key)
	./deploy/scripts/spin-down-worker.sh $(HOST) $(SSH_KEY) $(if $(filter true 1 yes,$(CLEAR)),--clear) $(if $(TIMEOUT),--timeout $(TIMEOUT)) $(if $(EXECUTOR_TIMEOUT),--executor-timeout $(EXECUTOR_TIMEOUT))

TAG ?= latest
ROLES ?= app,worker
force ?=
DEPLOY_JOBS ?= 4
WORKER_BLUE_GREEN_PORT_START ?= 8080
WORKER_BLUE_GREEN_PORT_END ?= 8087

deploy-force-env = FORCE_DEPLOY_WITH_ACTIVE_SESSIONS=$(if $(filter true 1 yes,$(force)),1,$(FORCE_DEPLOY_WITH_ACTIVE_SESSIONS))
worker-blue-green-env = WORKER_BLUE_GREEN_PORT_START=$(WORKER_BLUE_GREEN_PORT_START) WORKER_BLUE_GREEN_PORT_END=$(WORKER_BLUE_GREEN_PORT_END)

# Deploy (update) an already-provisioned node.
# HOST is optional — falls back to the matching role in FLEET_HOSTS from .env.production.enc.
# SSH_KEY is auto-detected from ~/.ssh/143-deploy but can be overridden.
# Usage:
#   make deploy-app
#   make deploy-app    HOST=87.99.150.138
#   make deploy-app    TAG=<sha>
#   make deploy-worker force=true
#   make deploy-fleet ROLES=app,worker

# Shell snippet to read FLEET_HOSTS from env var or $(_PROD_ENC) via SOPS.
# Sets $$FLEET. Use inside a recipe with: $(read-fleet-hosts);
define read-fleet-hosts
FLEET="$${FLEET_HOSTS:-}"; \
if [ -z "$$FLEET" ]; then \
	FLEET="$$(sops --decrypt --input-type dotenv --output-type dotenv $(_PROD_ENC) 2>/dev/null | grep '^FLEET_HOSTS=' | cut -d= -f2- || true)"; \
fi
endef

# Resolve HOST(s) from FLEET_HOSTS for a given role and deploy each.
# If HOST is set explicitly, deploys to that single host.
# Otherwise, deploys to ALL hosts matching the role in FLEET_HOSTS.
# Usage: @$(call resolve-host,role)
define resolve-host
if [ -n "$(HOST)" ]; then \
	echo "Deploying $(1) → $(HOST)"; \
	$(worker-blue-green-env) $(deploy-force-env) ./deploy/scripts/deploy.sh $(1) $(HOST) $(SSH_KEY) $(TAG); \
else \
	$(read-fleet-hosts); \
	HOSTS="$$(echo "$$FLEET" | tr ',' '\n' | grep '^$(1):' | cut -d: -f2)"; \
	if [ -z "$$HOSTS" ]; then \
		echo "ERROR: Could not resolve host for role '$(1)'. Set HOST or add $(1):<ip> to FLEET_HOSTS."; \
		exit 1; \
	fi; \
	for h in $$HOSTS; do \
		echo "Deploying $(1) → $$h"; \
		$(worker-blue-green-env) $(deploy-force-env) ./deploy/scripts/deploy.sh $(1) $$h $(SSH_KEY) $(TAG); \
	done; \
fi
endef

deploy-app:
	$(check-ssh-key)
	@$(call resolve-host,app)

deploy-worker:
	$(check-ssh-key)
	@$(call resolve-host,worker)

deploy-worker-preflight:
	$(check-ssh-key)
	@if [ -n "$(HOST)" ]; then \
		HOSTS="$(HOST)"; \
	else \
		$(read-fleet-hosts); \
		HOSTS="$$(echo "$$FLEET" | tr ',' '\n' | grep '^worker:' | cut -d: -f2)"; \
	fi; \
	if [ -z "$$HOSTS" ]; then \
		echo "ERROR: no worker host in FLEET_HOSTS; set HOST=<worker-host> or add worker:<ip> to FLEET_HOSTS"; exit 1; \
	fi; \
	for h in $$HOSTS; do \
		echo "=== worker preflight $$h ==="; \
		WORKER_BLUE_GREEN_PORT_START="$(WORKER_BLUE_GREEN_PORT_START)" \
		WORKER_BLUE_GREEN_PORT_END="$(WORKER_BLUE_GREEN_PORT_END)" \
		bash ./deploy/scripts/deploy-worker-preflight.sh $$h $(SSH_KEY); \
	done

# Tail the latest detached worker rollover log on each worker host.
# Useful after a CI deploy (which runs with WORKER_DEPLOY_DETACH=1) to see
# whether the rollover finished, or after `make deploy-worker WORKER_DEPLOY_DETACH=1`.
# Pass FOLLOW=1 to `tail -f` the most recent log instead of dumping the tail.
deploy-worker-status:
	$(check-ssh-key)
	@$(read-fleet-hosts); \
	HOSTS="$$(echo "$$FLEET" | tr ',' '\n' | grep '^worker:' | cut -d: -f2)"; \
	if [ -z "$$HOSTS" ]; then \
		echo "ERROR: no worker host in FLEET_HOSTS"; exit 1; \
	fi; \
	for h in $$HOSTS; do \
		echo "=== worker $$h ==="; \
		if [ -n "$(FOLLOW)" ]; then \
			ssh -i $(SSH_KEY) -t deploy@$$h 'f=$$(ls -1t /var/log/143/deploy-worker-*.log 2>/dev/null | head -1); [ -n "$$f" ] && { echo "tailing $$f"; exec tail -f "$$f"; } || echo "no detached deploy logs found"'; \
		else \
			ssh -i $(SSH_KEY) deploy@$$h 'ls -1t /var/log/143/deploy-worker-*.log 2>/dev/null | head -3 | while read f; do echo "--- $$f ---"; tail -30 "$$f"; done || echo "no detached deploy logs found"'; \
		fi; \
	done

deploy-db:
	$(check-ssh-key)
	@$(call resolve-host,db)

deploy-logging:
	$(check-ssh-key)
	@$(call resolve-host,logging)

deploy-redis:
	$(check-ssh-key)
	@$(call resolve-host,redis)

# Deploy app+worker nodes in the fleet by default.
# Uses FLEET_HOSTS env var or FLEET_HOSTS in .env.production.enc.
# For explicit maintenance deploys of every role:
#   make deploy-fleet ROLES=all
# For a specific image tag:
#   make deploy-fleet TAG=<sha> ROLES=app,worker
# To override the active-session guardrail:
#   make deploy-fleet force=true
# To serialize node deploys:
#   make deploy-fleet DEPLOY_JOBS=1
deploy-fleet:
	$(check-ssh-key)
	$(worker-blue-green-env) $(deploy-force-env) DEPLOY_JOBS=$(DEPLOY_JOBS) ./deploy/scripts/deploy-fleet.sh $(SSH_KEY) $(TAG) $(ROLES)

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

# One-shot LogsQL query against VictoriaLogs. Prints NDJSON results to stdout
# (one JSON log line per result). Bounded by a 30s curl timeout. The query
# text travels over ssh stdin so shell metacharacters cannot escape.
#
# Usage: make logs-query Q='service:api AND level:error AND _time:[now-1h,now]' [LIMIT=100]
#
# Add `_time:[now-1h,now]` (or similar) to bound the time range, otherwise
# VictoriaLogs scans the full retention window. LogsQL reference:
# https://docs.victoriametrics.com/victorialogs/logsql/
logs-query: export LOGS_QUERY := $(Q)
logs-query: export LOGS_LIMIT := $(or $(LIMIT),100)
logs-query:
	@test -n "$$LOGS_QUERY" || { echo "Usage: make logs-query Q='service:api AND level:error' [LIMIT=100]"; exit 1; }
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
	printf '%s' "$$LOGS_QUERY" | ssh -i $(SSH_KEY) deploy@$$LOGGING_HOST \
	  "VLOGS_HOST=\$$(grep ^VICTORIALOGS_HOST= /opt/143/.env | cut -d= -f2); \
	   curl -sS --max-time 30 \
	     --data-urlencode query@- \
	     --data-urlencode limit=$$LOGS_LIMIT \
	     http://\$$VLOGS_HOST:9428/select/logsql/query"

# ── Read-only prod DB access ─────────────────────────────────────────
# The `readonly` Postgres role is safe for ad-hoc inspection by humans and
# coding agents: SELECT only, every connection opens a read-only txn, and
# a 30s statement_timeout bounds runaway queries.

# $(call resolve-db-and-readonly-env,REQUIRE_ADMIN)
# Resolves DB_HOST from FLEET_HOSTS and DB_READONLY_PASSWORD (+ DB_PASSWORD
# when REQUIRE_ADMIN is non-empty) from .env.production.enc into exported
# shell vars. Use inside a recipe joined with `\` so the vars persist.
define resolve-db-and-readonly-env
DB_HOST="$${DB_HOST:-}"; \
DB_PASSWORD="$${DB_PASSWORD:-}"; \
DB_READONLY_PASSWORD="$${DB_READONLY_PASSWORD:-}"; \
if [ -z "$$DB_HOST" ] || [ -z "$$DB_PASSWORD" ] || [ -z "$$DB_READONLY_PASSWORD" ]; then \
	ENV_DUMP="$$(sops --decrypt --input-type dotenv --output-type dotenv $(_PROD_ENC) 2>/dev/null || true)"; \
	if [ -z "$$DB_HOST" ]; then \
		FLEET="$$(printf '%s\n' "$$ENV_DUMP" | grep '^FLEET_HOSTS=' | cut -d= -f2-)"; \
		DB_HOST="$$(echo "$$FLEET" | tr ',' '\n' | grep '^db:' | cut -d: -f2 | head -1)"; \
	fi; \
	if [ -z "$$DB_PASSWORD" ]; then \
		DB_PASSWORD="$$(printf '%s\n' "$$ENV_DUMP" | grep '^DB_PASSWORD=' | cut -d= -f2-)"; \
	fi; \
	if [ -z "$$DB_READONLY_PASSWORD" ]; then \
		DB_READONLY_PASSWORD="$$(printf '%s\n' "$$ENV_DUMP" | grep '^DB_READONLY_PASSWORD=' | cut -d= -f2-)"; \
	fi; \
fi; \
if [ -z "$$DB_HOST" ]; then echo "ERROR: DB_HOST not set. Add db:<ip> to FLEET_HOSTS in .env.production.enc."; exit 1; fi; \
if [ -z "$$DB_READONLY_PASSWORD" ]; then echo "ERROR: DB_READONLY_PASSWORD not set. Add to .env.production.enc (use 'make secrets-edit ENV=production')."; exit 1; fi; \
if [ -n "$(1)" ] && [ -z "$$DB_PASSWORD" ]; then echo "ERROR: DB_PASSWORD (admin) not set. Needed to apply the readonly role."; exit 1; fi; \
export DB_HOST DB_PASSWORD DB_READONLY_PASSWORD
endef

# Run once to create the readonly role on prod. Rerun only to rotate the
# password (the SQL is idempotent).
setup-readonly-user:
	$(check-ssh-key)
	@$(call resolve-db-and-readonly-env,admin); \
	DB_HOST="$$DB_HOST" \
	DB_PASSWORD="$$DB_PASSWORD" \
	DB_READONLY_PASSWORD="$$DB_READONLY_PASSWORD" \
	SSH_KEY="$(SSH_KEY)" \
	./deploy/scripts/setup-readonly-user.sh

# Interactive read-only psql session on prod.
# Usage: make db-psql
db-psql:
	$(check-ssh-key)
	@$(call resolve-db-and-readonly-env); \
	echo "Connecting to $$DB_HOST as readonly (statement_timeout=30s — 'SET statement_timeout=0' to disable)..."; \
	ssh -i $(SSH_KEY) -t deploy@$$DB_HOST \
	  "docker exec -it -e PGPASSWORD='$$DB_READONLY_PASSWORD' -e PGOPTIONS='-c statement_timeout=30000' 143-postgres-1 \
	     psql -U readonly -d onefortythree"

# One-shot read-only query. Prints results to stdout. Bounded by a 30s
# statement timeout (connection-level PGOPTIONS).
# Usage: make db-query Q='SELECT id,status FROM sessions ORDER BY created_at DESC LIMIT 5'
#
# The query text is passed through a target-specific exported env var so
# shell metacharacters inside Q (quotes, semicolons, backticks) cannot
# escape into the recipe shell. Note: Make still eats single `$` — use
# `$$` on the command line for a literal dollar sign (e.g. `$$1`, `$$foo`).
db-query: export DB_QUERY_SQL := $(Q)
db-query:
	@test -n "$$DB_QUERY_SQL" || { echo "Usage: make db-query Q='SELECT ...'"; exit 1; }
	$(check-ssh-key)
	@$(call resolve-db-and-readonly-env); \
	printf '%s\n' "$$DB_QUERY_SQL" | ssh -i $(SSH_KEY) deploy@$$DB_HOST \
	  "docker exec -i -e PGPASSWORD='$$DB_READONLY_PASSWORD' -e PGOPTIONS='-c statement_timeout=30000' 143-postgres-1 \
	     psql -U readonly -d onefortythree -v ON_ERROR_STOP=1"
