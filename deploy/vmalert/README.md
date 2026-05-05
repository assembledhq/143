`production-alerts.yml` is the repo-owned source of truth for log-derived production alerts.

Use `vmalert` with VictoriaLogs for these rules instead of Grafana-managed alert rules. The current `victoriametrics-logs-datasource` is good for exploration and dashboards, but it is not a reliable target for provisioning Grafana-managed log alerts end to end.

Recommended runtime topology:

- `vmalert` evaluates `deploy/vmalert/rules/production-alerts.yml` against VictoriaLogs
- `Alertmanager` handles grouping and delivery
- `Grafana` uses the provisioned `Alertmanager` datasource as the operator UI for active notifications

Before enabling the rules, add a dedicated scheduler heartbeat log line and either a queue backlog metric or a backlog log signal so those two missing alerts can be added without guessing.

The logging compose stack now includes both `vmalert` and `Alertmanager`. Notification delivery is configured from the logging node environment via:

- `GRAFANA_ALERTS_WARNING_WEBHOOK_URL`
- `GRAFANA_ALERTS_CRITICAL_WEBHOOK_URL`

Alertmanager now posts to an internal `alert-slack-relay` service, and that relay translates Alertmanager webhook payloads into the plain `{"text": ...}` shape Slack incoming webhooks expect.

If either webhook URL is omitted, the relay falls back to a disabled localhost endpoint instead of failing provisioning or rollout. Alerts for that severity will be dropped until a real webhook URL is configured.
