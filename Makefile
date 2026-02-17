.PHONY: dev setup test test-coverage migrate-up migrate-down build frontend-dev frontend-lint frontend-typecheck frontend-check lint

dev:
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
