#!/usr/bin/env bash
set -euo pipefail

SERVICE_USER="143-connector"
INSTALL_DIR="${143_CONNECTOR_INSTALL_DIR:-/usr/local/bin}"
CONFIG_DIR="${143_CONNECTOR_CONFIG_DIR:-/etc/143}"
STATE_DIR="${143_CONNECTOR_STATE_DIR:-/var/lib/143-connector}"
SERVICE_NAME="${143_CONNECTOR_SERVICE_NAME:-143-private-connector}"
ENV_FILE="$CONFIG_DIR/private-connector.env"
API_URL="${143_API_URL:-https://app.143.dev}"
VERSION="${143_CONNECTOR_VERSION:-latest}"
DOWNLOAD_BASE="${143_CONNECTOR_DOWNLOAD_BASE:-https://get.143.dev/private-connector}"
LOCAL_BINARY="${143_CONNECTOR_LOCAL_BINARY:-}"
PROVIDERS="${143_CONNECTOR_PROVIDERS:-victorialogs}"

# This key must be replaced by release automation before publishing to get.143.dev.
# The installer intentionally keeps the trust anchor inline rather than fetching it
# from the same host as the binary.
COSIGN_PUBLIC_KEY_B64="${143_CONNECTOR_COSIGN_PUBLIC_KEY_B64:-}"

log() {
  printf '%s\n' "$*"
}

fail() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

need_root() {
  if [ "$(id -u)" -ne 0 ]; then
    fail "run as root, for example: curl -fsSL https://get.143.dev/private-connector.sh | sudo 143_CONNECTOR_TOKEN=... bash"
  fi
}

read_token() {
  if [ -n "${143_CONNECTOR_TOKEN:-}" ]; then
    return
  fi
  if [ -n "${143_CONNECTOR_TOKEN_FILE:-}" ]; then
    [ -f "$143_CONNECTOR_TOKEN_FILE" ] || fail "143_CONNECTOR_TOKEN_FILE does not exist"
    143_CONNECTOR_TOKEN="$(tr -d '\n\r\t ' < "$143_CONNECTOR_TOKEN_FILE")"
    export 143_CONNECTOR_TOKEN
  fi
  if [ -z "${143_CONNECTOR_TOKEN:-}" ] && [ ! -f "$STATE_DIR/state.json" ]; then
    fail "143_CONNECTOR_TOKEN or 143_CONNECTOR_TOKEN_FILE is required for first registration"
  fi
}

detect_platform() {
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"
  case "$os" in
    linux) ;;
    *) fail "unsupported operating system: $os" ;;
  esac
  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) fail "unsupported architecture: $arch" ;;
  esac
}

provider_artifact_name() {
  provider_set="$(
    printf '%s' "$PROVIDERS" |
      tr ',' '\n' |
      sed 's/^[[:space:]]*//;s/[[:space:]]*$//' |
      sed '/^$/d' |
      sort -u |
      paste -sd, -
  )"
  case "$provider_set" in
    victorialogs)
      printf '%s\n' "private-connector-victorialogs"
      ;;
    postgres)
      printf '%s\n' "private-connector-postgres"
      ;;
    postgres,victorialogs|victorialogs,postgres)
      printf '%s\n' "private-connector"
      ;;
    *)
      fail "unsupported 143_CONNECTOR_PROVIDERS value: $PROVIDERS"
      ;;
  esac
}

install_prereqs() {
  command -v install >/dev/null 2>&1 || fail "install command is required"
  command -v systemctl >/dev/null 2>&1 || fail "systemd is required for the default installer"
  command -v runuser >/dev/null 2>&1 || command -v su >/dev/null 2>&1 || fail "runuser or su is required to bootstrap as $SERVICE_USER"
  if [ -z "$LOCAL_BINARY" ]; then
    command -v curl >/dev/null 2>&1 || fail "curl is required to download the connector"
    command -v cosign >/dev/null 2>&1 || fail "cosign is required to verify the connector binary"
    [ -n "$COSIGN_PUBLIC_KEY_B64" ] || fail "connector signing public key is not embedded in this installer"
  fi
}

ensure_user() {
  if ! id "$SERVICE_USER" >/dev/null 2>&1; then
    useradd --system --home "$STATE_DIR" --shell /usr/sbin/nologin "$SERVICE_USER"
  fi
}

write_config() {
  install -d -m 0755 "$CONFIG_DIR"
  if [ ! -f "$CONFIG_DIR/connector.yaml" ]; then
    cat > "$CONFIG_DIR/connector.yaml" <<EOF
api_url: "$API_URL"
providers: "$PROVIDERS"
state_path: "$STATE_DIR/state.json"
identity_path: "$STATE_DIR/identity.key"
EOF
    chmod 0644 "$CONFIG_DIR/connector.yaml"
  fi
}

write_env_file() {
  install -d -m 0755 "$CONFIG_DIR"
  : > "$ENV_FILE"
  chmod 0600 "$ENV_FILE"
  write_env "143_API_URL" "$API_URL"
  write_env "143_CONNECTOR_IDENTITY_PATH" "$STATE_DIR/identity.key"
  write_env "143_CONNECTOR_STATE_PATH" "$STATE_DIR/state.json"
  write_env "143_CONNECTOR_REGION" "${143_CONNECTOR_REGION:-us}"
  write_env "143_CONNECTOR_GATEWAY_PUBLIC_KEY" "${143_CONNECTOR_GATEWAY_PUBLIC_KEY:-}"
  write_env "143_CONNECTOR_UPDATE_COMMAND" "${143_CONNECTOR_UPDATE_COMMAND:-}"
  write_env "143_CONNECTOR_VICTORIALOGS_RESOURCE_ID" "${143_CONNECTOR_VICTORIALOGS_RESOURCE_ID:-}"
  write_env "143_CONNECTOR_VICTORIALOGS_URL" "${143_CONNECTOR_VICTORIALOGS_URL:-}"
  write_env "143_CONNECTOR_VICTORIALOGS_FIELD_NAMES_URL" "${143_CONNECTOR_VICTORIALOGS_FIELD_NAMES_URL:-}"
  write_env "143_CONNECTOR_VICTORIALOGS_MAX_ROWS" "${143_CONNECTOR_VICTORIALOGS_MAX_ROWS:-}"
  write_env "143_CONNECTOR_VICTORIALOGS_MAX_QUERY_WINDOW" "${143_CONNECTOR_VICTORIALOGS_MAX_QUERY_WINDOW:-}"
  write_env "143_CONNECTOR_VICTORIALOGS_MAX_TIME_RANGE" "${143_CONNECTOR_VICTORIALOGS_MAX_TIME_RANGE:-}"
  write_env "143_CONNECTOR_VICTORIALOGS_MAX_SERIES_CARDINALITY" "${143_CONNECTOR_VICTORIALOGS_MAX_SERIES_CARDINALITY:-}"
  write_env "143_CONNECTOR_VICTORIALOGS_MAX_REQUESTS_PER_MINUTE" "${143_CONNECTOR_VICTORIALOGS_MAX_REQUESTS_PER_MINUTE:-}"
  write_env "143_CONNECTOR_VICTORIALOGS_DEFAULT_FILTER" "${143_CONNECTOR_VICTORIALOGS_DEFAULT_FILTER:-}"
  write_env "143_CONNECTOR_VICTORIALOGS_ALLOWED_FIELDS" "${143_CONNECTOR_VICTORIALOGS_ALLOWED_FIELDS:-}"
  write_env "143_CONNECTOR_VICTORIALOGS_DENIED_FIELDS" "${143_CONNECTOR_VICTORIALOGS_DENIED_FIELDS:-}"
  write_env "143_CONNECTOR_VICTORIALOGS_REDACT_FIELDS" "${143_CONNECTOR_VICTORIALOGS_REDACT_FIELDS:-}"
  write_env "143_CONNECTOR_VICTORIALOGS_TOKEN_FILE" "${143_CONNECTOR_VICTORIALOGS_TOKEN_FILE:-}"
  write_env "143_CONNECTOR_VICTORIALOGS_TOKEN" "${143_CONNECTOR_VICTORIALOGS_TOKEN:-}"
  write_env "143_CONNECTOR_POSTGRES_RESOURCE_ID" "${143_CONNECTOR_POSTGRES_RESOURCE_ID:-}"
  write_env "143_CONNECTOR_POSTGRES_DATABASE_URL_FILE" "${143_CONNECTOR_POSTGRES_DATABASE_URL_FILE:-}"
  write_env "143_CONNECTOR_POSTGRES_DATABASE_URL" "${143_CONNECTOR_POSTGRES_DATABASE_URL:-}"
  write_env "143_CONNECTOR_POSTGRES_MAX_ROWS" "${143_CONNECTOR_POSTGRES_MAX_ROWS:-}"
  write_env "143_CONNECTOR_POSTGRES_STATEMENT_TIMEOUT_MS" "${143_CONNECTOR_POSTGRES_STATEMENT_TIMEOUT_MS:-}"
  write_env "143_CONNECTOR_POSTGRES_REDACT_COLUMNS" "${143_CONNECTOR_POSTGRES_REDACT_COLUMNS:-}"
}

write_env() {
  key="$1"
  value="$2"
  if [ -n "$value" ]; then
    printf '%s=%q\n' "$key" "$value" >> "$ENV_FILE"
  fi
}

download_and_verify() {
  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' EXIT
  target="$tmpdir/private-connector"
  if [ -n "$LOCAL_BINARY" ]; then
    [ -x "$LOCAL_BINARY" ] || fail "143_CONNECTOR_LOCAL_BINARY must point to an executable file"
    cp "$LOCAL_BINARY" "$target"
  else
    artifact="$(provider_artifact_name)_${VERSION}_${os}_${arch}"
    url="$DOWNLOAD_BASE/$VERSION/$artifact"
    sig_url="$url.sig"
    key_file="$tmpdir/cosign.pub"
    printf '%s' "$COSIGN_PUBLIC_KEY_B64" | base64 -d > "$key_file"
    curl -fsSL "$url" -o "$target"
    curl -fsSL "$sig_url" -o "$target.sig"
    cosign verify-blob --key "$key_file" --signature "$target.sig" "$target" >/dev/null
  fi
  install -m 0755 "$target" "$INSTALL_DIR/143-private-connector"
}

write_systemd_unit() {
  install -d -m 0700 -o "$SERVICE_USER" -g "$SERVICE_USER" "$STATE_DIR"
  cat > "/etc/systemd/system/$SERVICE_NAME.service" <<EOF
[Unit]
Description=143 Private Connector
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_USER
EnvironmentFile=$ENV_FILE
ExecStart=$INSTALL_DIR/143-private-connector
Restart=always
RestartSec=5s
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=$STATE_DIR

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "/etc/systemd/system/$SERVICE_NAME.service"
}

run_as_connector() {
  if command -v runuser >/dev/null 2>&1; then
    runuser -u "$SERVICE_USER" -- "$@"
  else
    su -s /bin/sh "$SERVICE_USER" -c "$(printf '%q ' "$@")"
  fi
}

bootstrap_connector() {
  if [ -f "$STATE_DIR/state.json" ]; then
    return
  fi
  run_as_connector env \
    "143_API_URL=$API_URL" \
    "143_CONNECTOR_TOKEN=$143_CONNECTOR_TOKEN" \
    "143_CONNECTOR_IDENTITY_PATH=$STATE_DIR/identity.key" \
    "143_CONNECTOR_STATE_PATH=$STATE_DIR/state.json" \
    "143_CONNECTOR_REGION=${143_CONNECTOR_REGION:-us}" \
    "143_CONNECTOR_GATEWAY_PUBLIC_KEY=${143_CONNECTOR_GATEWAY_PUBLIC_KEY:-}" \
    "143_CONNECTOR_UPDATE_COMMAND=${143_CONNECTOR_UPDATE_COMMAND:-}" \
    "143_CONNECTOR_VICTORIALOGS_RESOURCE_ID=${143_CONNECTOR_VICTORIALOGS_RESOURCE_ID:-}" \
    "143_CONNECTOR_VICTORIALOGS_URL=${143_CONNECTOR_VICTORIALOGS_URL:-}" \
    "143_CONNECTOR_VICTORIALOGS_FIELD_NAMES_URL=${143_CONNECTOR_VICTORIALOGS_FIELD_NAMES_URL:-}" \
    "143_CONNECTOR_VICTORIALOGS_MAX_ROWS=${143_CONNECTOR_VICTORIALOGS_MAX_ROWS:-}" \
    "143_CONNECTOR_VICTORIALOGS_MAX_QUERY_WINDOW=${143_CONNECTOR_VICTORIALOGS_MAX_QUERY_WINDOW:-}" \
    "143_CONNECTOR_VICTORIALOGS_MAX_TIME_RANGE=${143_CONNECTOR_VICTORIALOGS_MAX_TIME_RANGE:-}" \
    "143_CONNECTOR_VICTORIALOGS_MAX_SERIES_CARDINALITY=${143_CONNECTOR_VICTORIALOGS_MAX_SERIES_CARDINALITY:-}" \
    "143_CONNECTOR_VICTORIALOGS_MAX_REQUESTS_PER_MINUTE=${143_CONNECTOR_VICTORIALOGS_MAX_REQUESTS_PER_MINUTE:-}" \
    "143_CONNECTOR_VICTORIALOGS_DEFAULT_FILTER=${143_CONNECTOR_VICTORIALOGS_DEFAULT_FILTER:-}" \
    "143_CONNECTOR_VICTORIALOGS_ALLOWED_FIELDS=${143_CONNECTOR_VICTORIALOGS_ALLOWED_FIELDS:-}" \
    "143_CONNECTOR_VICTORIALOGS_DENIED_FIELDS=${143_CONNECTOR_VICTORIALOGS_DENIED_FIELDS:-}" \
    "143_CONNECTOR_VICTORIALOGS_REDACT_FIELDS=${143_CONNECTOR_VICTORIALOGS_REDACT_FIELDS:-}" \
    "143_CONNECTOR_VICTORIALOGS_TOKEN_FILE=${143_CONNECTOR_VICTORIALOGS_TOKEN_FILE:-}" \
    "143_CONNECTOR_VICTORIALOGS_TOKEN=${143_CONNECTOR_VICTORIALOGS_TOKEN:-}" \
    "143_CONNECTOR_POSTGRES_RESOURCE_ID=${143_CONNECTOR_POSTGRES_RESOURCE_ID:-}" \
    "143_CONNECTOR_POSTGRES_DATABASE_URL_FILE=${143_CONNECTOR_POSTGRES_DATABASE_URL_FILE:-}" \
    "143_CONNECTOR_POSTGRES_DATABASE_URL=${143_CONNECTOR_POSTGRES_DATABASE_URL:-}" \
    "143_CONNECTOR_POSTGRES_MAX_ROWS=${143_CONNECTOR_POSTGRES_MAX_ROWS:-}" \
    "143_CONNECTOR_POSTGRES_STATEMENT_TIMEOUT_MS=${143_CONNECTOR_POSTGRES_STATEMENT_TIMEOUT_MS:-}" \
    "143_CONNECTOR_POSTGRES_REDACT_COLUMNS=${143_CONNECTOR_POSTGRES_REDACT_COLUMNS:-}" \
    "$INSTALL_DIR/143-private-connector" --register-only
  unset 143_CONNECTOR_TOKEN
}

main() {
  need_root
  read_token
  detect_platform
  install_prereqs
  ensure_user
  install -d -m 0755 "$INSTALL_DIR"
  download_and_verify
  write_config
  write_env_file
  write_systemd_unit
  bootstrap_connector
  systemctl daemon-reload
  systemctl enable "$SERVICE_NAME.service" >/dev/null
  systemctl restart "$SERVICE_NAME.service"
  log "143 Private Connector installed."
  log "Config: $CONFIG_DIR/connector.yaml"
  log "Status: systemctl status $SERVICE_NAME.service"
}

main "$@"
