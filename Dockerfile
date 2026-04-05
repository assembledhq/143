# syntax=docker/dockerfile:1

# Stage 1: Go builder
FROM golang:1.26 AS go-builder
WORKDIR /app
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
ARG BUILD_SHA=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags "-X github.com/assembledhq/143/internal/version.BuildSHA=${BUILD_SHA}" -o /bin/server ./cmd/server && \
    CGO_ENABLED=0 go build -o /bin/migrate ./cmd/migrate

# Stage 2: Runtime
FROM debian:bookworm-slim

# Install runtime deps + sops/age for encrypted secrets decryption at boot
RUN apt-get update && apt-get install -y ca-certificates wget && rm -rf /var/lib/apt/lists/* \
    && ARCH=$(dpkg --print-architecture) \
    && wget -qO /usr/local/bin/sops "https://github.com/getsops/sops/releases/download/v3.9.4/sops-v3.9.4.linux.${ARCH}" \
    && chmod +x /usr/local/bin/sops \
    && wget -qO /tmp/age.tar.gz "https://dl.filippo.io/age/v1.2.0?for=linux/${ARCH}" \
    && tar -xzf /tmp/age.tar.gz -C /tmp \
    && mv /tmp/age/age /usr/local/bin/age \
    && mv /tmp/age/age-keygen /usr/local/bin/age-keygen \
    && rm -rf /tmp/age /tmp/age.tar.gz

COPY --from=go-builder /bin/server /bin/server
COPY --from=go-builder /bin/migrate /bin/migrate
COPY --from=go-builder /app/migrations /migrations

# Copy entrypoint and encrypted production secrets
COPY docker-entrypoint.sh /docker-entrypoint.sh
COPY .env.production.enc .env.production.enc

EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://localhost:8080/healthz || exit 1

RUN useradd -r -s /usr/sbin/nologin appuser
USER appuser

ENTRYPOINT ["/docker-entrypoint.sh"]
CMD ["/bin/server"]
