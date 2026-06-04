# Worker Spin-Down Operations

> Status: Implemented | Last reviewed: 2026-05-28

Production workers have an explicit operator path for decommissioning or
maintenance:

```bash
make spin-down-worker HOST=<worker-host>
make spin-down-worker HOST=<worker-host> CLEAR=true
make spin-down-worker HOST=<worker-host> TIMEOUT=7200 EXECUTOR_TIMEOUT=600
```

The default path drains every running worker generation on the host by sending
`SIGTERM` and waiting up to the worker drain timeout. This reuses the normal
worker shutdown behavior for active coding turns and preview runtimes before
the script stops the worker compose stack.

Durable session executor containers are also given their own bounded stop
window so in-flight executor-owned turns can observe shutdown before the host is
cleared.

Machine clearing is opt-in. `CLEAR=true` removes remaining session executor
containers, managed sandbox containers, stopped containers, unused volumes, and
unused Docker images/build cache. The script does not remove `/opt/143` or host
provisioning files, so a worker can be redeployed or reprovisioned after
cleanup.
