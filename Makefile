# Stamp file tracking when the sandbox image was last built.
SANDBOX_STAMP := sandbox/.build-stamp
SANDBOX_SOURCES := sandbox/Dockerfile sandbox/versions.json

.PHONY: dev dev-ngrok dev-local dev-frontend-only setup test test-coverage migrate-up migrate-down build frontend-dev frontend-lint frontend-typecheck frontend-check lint lint-bootstrap secrets-setup secrets-encrypt secrets-decrypt secrets-edit secrets-rotate

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

lint:
	@$(MAKE) lint-bootstrap
	$(GOLANGCI_LINT_BIN) run ./...

lint-bootstrap:
	@mkdir -p $(CURDIR)/bin
	GOTOOLCHAIN=$(GO_TOOLCHAIN_VERSION) GOBIN=$(CURDIR)/bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

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

# Resolve file names from ENV. "" → .env / .env.enc, "staging" → .env.staging / .env.staging.enc
ifdef ENV
  _ENV_FILE     := .env.$(ENV)
  _ENV_ENC_FILE := .env.$(ENV).enc
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
		sops updatekeys --yes "$$f"; \
	done
	@echo "Done. Commit the updated .enc files."
