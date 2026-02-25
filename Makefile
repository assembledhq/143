.PHONY: dev dev-local dev-frontend-only setup test test-coverage migrate-up migrate-down build frontend-dev frontend-lint frontend-typecheck frontend-check lint lint-bootstrap secrets-setup secrets-encrypt secrets-decrypt secrets-edit

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
	docker compose up --build

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

build:
	go build -o bin/server ./cmd/server
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
	go run cmd/server/main.go

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
# How it works:
#   1. make secrets-setup      — generate an age keypair (one-time)
#   2. Fill in .env with real values
#   3. make secrets-encrypt    — encrypt .env → .env.enc (committed to git)
#   4. make secrets-decrypt    — decrypt .env.enc → .env (on deploy/new machine)
#   5. make secrets-edit       — edit encrypted secrets in-place

SOPS_AGE_KEY_FILE ?= $(HOME)/.config/sops/age/keys.txt

secrets-setup:
	@command -v age-keygen >/dev/null 2>&1 || { echo "Install age: brew install age"; exit 1; }
	@command -v sops >/dev/null 2>&1 || { echo "Install sops: brew install sops"; exit 1; }
	@if [ -f $(SOPS_AGE_KEY_FILE) ]; then \
		echo "age key already exists at $(SOPS_AGE_KEY_FILE)"; \
		echo "Public key:"; grep "public key:" $(SOPS_AGE_KEY_FILE) | awk '{print $$4}'; \
	else \
		mkdir -p $$(dirname $(SOPS_AGE_KEY_FILE)); \
		age-keygen -o $(SOPS_AGE_KEY_FILE) 2>&1; \
		echo ""; \
		echo "Key saved to $(SOPS_AGE_KEY_FILE)"; \
		echo "Add the public key above to .sops.yaml in the repo."; \
		echo "Keep $(SOPS_AGE_KEY_FILE) safe — it's your decryption key."; \
	fi

secrets-encrypt:
	@test -f .env || { echo "No .env file to encrypt. Copy .env.example to .env first."; exit 1; }
	@test -f .sops.yaml || { echo "No .sops.yaml found. Run make secrets-setup first."; exit 1; }
	sops --encrypt .env > .env.enc
	@echo "Encrypted .env → .env.enc (safe to commit)"

secrets-decrypt:
	@test -f .env.enc || { echo "No .env.enc file found."; exit 1; }
	sops --decrypt .env.enc > .env
	@echo "Decrypted .env.enc → .env"

secrets-edit:
	@test -f .env.enc || { echo "No .env.enc file found."; exit 1; }
	sops .env.enc
