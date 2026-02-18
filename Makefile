.PHONY: dev setup test test-coverage migrate-up migrate-down build frontend-dev frontend-lint frontend-typecheck frontend-check lint

dev:
	@FRONTEND_PORT=$${FRONTEND_PORT:-3000}; \
	if lsof -nP -iTCP:$$FRONTEND_PORT -sTCP:LISTEN >/dev/null 2>&1; then \
		echo "Port $$FRONTEND_PORT is already in use."; \
		echo "Stop the process on that port, or run: FRONTEND_PORT=3001 make dev"; \
		exit 1; \
	fi; \
	docker compose up --build

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
	golangci-lint run ./...
