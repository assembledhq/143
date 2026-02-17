.PHONY: dev setup test test-coverage migrate-up migrate-down build frontend-dev lint

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

server-dev:
	go run cmd/server/main.go

lint:
	golangci-lint run ./...
