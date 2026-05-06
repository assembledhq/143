#!/usr/bin/env python3
import json
import os
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any
from urllib import error, request


DISABLED_WEBHOOK_PREFIX = "http://localhost:65535/disabled-"
MAX_ALERT_LINES = 5


def log(level: str, message: str, **fields: Any) -> None:
    payload = {"level": level, "service": "alert-slack-relay", "message": message, **fields}
    print(json.dumps(payload), flush=True)


def first_non_empty(*values: Any) -> str:
    for value in values:
        if isinstance(value, str) and value.strip():
            return value.strip()
    return ""


def render_slack_text(route_severity: str, payload: dict[str, Any]) -> str:
    common_labels = payload.get("commonLabels") or {}
    common_annotations = payload.get("commonAnnotations") or {}
    alerts = payload.get("alerts") or []
    status = first_non_empty(payload.get("status"), "firing").upper()
    severity = first_non_empty(common_labels.get("severity"), route_severity).lower()
    alertname = first_non_empty(common_labels.get("alertname"), "143 production alert")
    service = first_non_empty(common_labels.get("service"), "unknown")
    summary = first_non_empty(common_annotations.get("summary"))
    description = first_non_empty(common_annotations.get("description"))

    lines = [
        f"*{status}* 143 production {severity} alert",
        f"*Alert*: {alertname}",
        f"*Service*: {service}",
        f"*Alerts in notification*: {len(alerts)}",
    ]

    if summary:
        lines.append(f"*Summary*: {summary}")
    if description:
        lines.append(f"*Description*: {description}")

    for index, alert in enumerate(alerts[:MAX_ALERT_LINES], start=1):
        labels = alert.get("labels") or {}
        annotations = alert.get("annotations") or {}
        item_summary = first_non_empty(
            annotations.get("summary"),
            annotations.get("description"),
            labels.get("instance"),
            labels.get("pod"),
            labels.get("job"),
        )
        if item_summary:
            lines.append(f"- {index}. {item_summary}")

        generator_url = first_non_empty(alert.get("generatorURL"))
        if generator_url:
            lines.append(generator_url)

    remaining_alerts = len(alerts) - MAX_ALERT_LINES
    if remaining_alerts > 0:
        lines.append(f"- ...and {remaining_alerts} more alerts")

    external_url = first_non_empty(payload.get("externalURL"))
    if external_url:
        lines.append(external_url)

    return "\n".join(lines)


def send_to_slack(slack_url: str, text: str) -> tuple[bool, str]:
    if not slack_url or slack_url.startswith(DISABLED_WEBHOOK_PREFIX):
        return True, "notification sink disabled"

    body = json.dumps({"text": text}).encode("utf-8")
    req = request.Request(
        slack_url,
        data=body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with request.urlopen(req, timeout=10) as resp:
            response_body = resp.read().decode("utf-8", errors="replace")
            if 200 <= resp.status < 300:
                return True, response_body
            return False, response_body
    except error.HTTPError as exc:
        return False, exc.read().decode("utf-8", errors="replace")
    except error.URLError as exc:
        return False, str(exc.reason)


class RelayHandler(BaseHTTPRequestHandler):
    webhook_urls = {
        "warning": os.environ.get("GRAFANA_ALERTS_WARNING_WEBHOOK_URL", ""),
        "critical": os.environ.get("GRAFANA_ALERTS_CRITICAL_WEBHOOK_URL", ""),
    }

    def do_GET(self) -> None:
        if self.path != "/healthz":
            self.send_error(HTTPStatus.NOT_FOUND, "not found")
            return
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.end_headers()
        self.wfile.write(b"ok")

    def do_POST(self) -> None:
        severity = self.path.strip("/")
        if severity not in self.webhook_urls:
            self.send_error(HTTPStatus.NOT_FOUND, "not found")
            return

        content_length = int(self.headers.get("Content-Length", "0"))
        raw_body = self.rfile.read(content_length)
        try:
            payload = json.loads(raw_body.decode("utf-8"))
        except json.JSONDecodeError:
            self.send_error(HTTPStatus.BAD_REQUEST, "invalid JSON payload")
            return

        slack_text = render_slack_text(severity, payload)
        ok, details = send_to_slack(self.webhook_urls[severity], slack_text)
        if not ok:
            log("error", "failed to forward alert notification to slack", severity=severity, details=details)
            self.send_response(HTTPStatus.BAD_GATEWAY)
            self.end_headers()
            self.wfile.write(b"failed to forward to slack")
            return

        log("info", "forwarded alert notification to slack", severity=severity, details=details)
        self.send_response(HTTPStatus.ACCEPTED)
        self.end_headers()
        self.wfile.write(b"accepted")

    def log_message(self, format: str, *args: Any) -> None:
        return


def main() -> int:
    port = int(os.environ.get("PORT", "8080"))
    server = ThreadingHTTPServer(("0.0.0.0", port), RelayHandler)
    log("info", "starting slack relay", port=port)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        log("info", "stopping slack relay")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
