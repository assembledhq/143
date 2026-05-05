#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
RELAY_SCRIPT="$ROOT_DIR/deploy/scripts/alertmanager_slack_relay.py"
COMPOSE_FILE="$ROOT_DIR/docker-compose.logging.yml"
CONTACT_POINTS_FILE="$ROOT_DIR/deploy/grafana/provisioning/alerting/contact-points.yml"

grep -q 'http://alert-slack-relay:8080/warning' "$COMPOSE_FILE"
grep -q 'http://alert-slack-relay:8080/critical' "$COMPOSE_FILE"
grep -q 'url: http://alert-slack-relay:8080/warning' "$CONTACT_POINTS_FILE"
grep -q 'url: http://alert-slack-relay:8080/critical' "$CONTACT_POINTS_FILE"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"; if [ -n "${SLACK_STUB_PID:-}" ]; then kill "$SLACK_STUB_PID" 2>/dev/null || true; fi; if [ -n "${RELAY_PID:-}" ]; then kill "$RELAY_PID" 2>/dev/null || true; fi' EXIT

SLACK_PAYLOAD_PATH="$TMP_DIR/slack-payload.json"
SLACK_PORT=18081
RELAY_PORT=18080

cat >"$TMP_DIR/slack_stub.py" <<'EOF'
import http.server
import pathlib
import sys

payload_path = pathlib.Path(sys.argv[1])
port = int(sys.argv[2])

class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        payload_path.write_bytes(self.rfile.read(length))
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"ok")

    def log_message(self, format, *args):
        return

http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
EOF

python3 "$TMP_DIR/slack_stub.py" "$SLACK_PAYLOAD_PATH" "$SLACK_PORT" &
SLACK_STUB_PID=$!

GRAFANA_ALERTS_WARNING_WEBHOOK_URL="http://127.0.0.1:${SLACK_PORT}/warning" \
GRAFANA_ALERTS_CRITICAL_WEBHOOK_URL="http://127.0.0.1:${SLACK_PORT}/critical" \
PORT="$RELAY_PORT" \
python3 "$RELAY_SCRIPT" &
RELAY_PID=$!

for _ in $(seq 1 50); do
  if curl -fsS "http://127.0.0.1:${RELAY_PORT}/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done

curl -fsS \
  -H 'Content-Type: application/json' \
  -d '{"receiver":"warning-slack","status":"firing","commonLabels":{"alertname":"API5xxWarning","service":"api","severity":"warning"},"commonAnnotations":{"summary":"API is returning elevated 5xx responses","description":"The warning threshold tripped."},"externalURL":"http://grafana.example","alerts":[{"status":"firing","labels":{"alertname":"API5xxWarning","service":"api","severity":"warning"},"annotations":{"summary":"API is returning elevated 5xx responses","description":"The warning threshold tripped."},"generatorURL":"http://grafana.example/alert-rule"}]}' \
  "http://127.0.0.1:${RELAY_PORT}/warning" >/dev/null

for _ in $(seq 1 50); do
  if [ -s "$SLACK_PAYLOAD_PATH" ]; then
    break
  fi
  sleep 0.1
done

python3 - "$SLACK_PAYLOAD_PATH" <<'EOF'
import json
import pathlib
import sys

payload = json.loads(pathlib.Path(sys.argv[1]).read_text())
text = payload.get("text", "")

assert "API5xxWarning" in text, text
assert "warning" in text.lower(), text
assert "API is returning elevated 5xx responses" in text, text
assert "http://grafana.example/alert-rule" in text, text
EOF

printf 'alertmanager slack relay test passed\n'
