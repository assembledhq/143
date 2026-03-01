# syntax=docker/dockerfile:1

# Stage 1: Go builder
FROM golang:1.26 AS go-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -o /bin/server ./cmd/server && \
    CGO_ENABLED=0 go build -o /bin/migrate ./cmd/migrate

# Stage 2: Runtime
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates wget && rm -rf /var/lib/apt/lists/*
COPY --from=go-builder /bin/server /bin/server
COPY --from=go-builder /bin/migrate /bin/migrate
COPY --from=go-builder /app/migrations /migrations
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://localhost:8080/healthz || exit 1

RUN useradd -r -s /usr/sbin/nologin appuser
USER appuser

ENTRYPOINT ["/bin/server"]
