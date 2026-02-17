# Stage 1: Go builder
FROM golang:1.24 AS go-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/server ./cmd/server
RUN CGO_ENABLED=0 go build -o /bin/migrate ./cmd/migrate

# Stage 2: Runtime
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=go-builder /bin/server /bin/server
COPY --from=go-builder /bin/migrate /bin/migrate
COPY --from=go-builder /app/migrations /migrations
EXPOSE 8080
ENTRYPOINT ["/bin/server"]
