# Stage 1: Go builder
FROM golang:1.24 AS go-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/server ./cmd/server
RUN CGO_ENABLED=0 go build -o /bin/migrate ./cmd/migrate

# Stage 2: Frontend builder
FROM node:22-alpine AS frontend-builder
WORKDIR /app
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

# Stage 3: Runtime
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=go-builder /bin/server /bin/server
COPY --from=go-builder /bin/migrate /bin/migrate
COPY --from=go-builder /app/migrations /migrations
COPY --from=frontend-builder /app/.next/standalone /frontend
COPY --from=frontend-builder /app/.next/static /frontend/.next/static
COPY --from=frontend-builder /app/public /frontend/public
EXPOSE 8080
ENTRYPOINT ["/bin/server"]
