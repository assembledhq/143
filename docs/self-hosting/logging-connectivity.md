# Verify log collector connectivity

A healthy Vector API confirms that the collector is running. It does not prove
that Vector can reach VictoriaLogs or that application logs are being ingested.
Validate these separately when adding a worker or changing network configuration.

Deployment checks the prospective collector configuration before application
migrations and worker rollout. Routine worker deployment also checks the retained
collector, because routine deployment does not refresh support-service
configuration. After rollout, deployment and provisioning check the actual
collector's effective environment and network. An unreachable sink fails these
checks; successful sink health only proves HTTP reachability.

From the deployment directory on an existing worker:

```sh
bash deploy/scripts/check-vector-sink.sh \
  "$(docker compose -f docker-compose.worker.yml ps -q vector)"
```

To check prospective Compose configuration without requiring the old collector
endpoint to work:

```sh
bash deploy/scripts/check-vector-sink.sh --compose docker-compose.worker.yml
```

The prospective check starts a temporary probe container using the Vector
service's environment and network. It does not run Vector or alter its disk
buffer. Both checks have bounded timeouts and avoid printing the endpoint.

If connectivity fails, verify the effective `VICTORIALOGS_HOST`, the collector's
route, the logging listener, and network access controls. Host-only connectivity
does not establish connectivity from the collector's Docker network. A private
endpoint also requires a private route from every collector that uses it; do not
assume that an overlay-connected worker can reach another network's private IP.

Changing the shared `VICTORIALOGS_HOST` also changes the logging stack's published
bind address. Review both collector destinations and listener exposure before
changing it. Apply approved endpoint changes with the maintenance deployment path
when the existing collector must be recreated. A prospective configuration check
can pass while a retained collector still uses its old environment.

After an approved repair, confirm recent application events arrive with the
expected host identity and timestamps. Check sink timeout/error/drop counts and
buffer behavior as well as health. Preserve disk buffers during diagnosis;
restoring reachability does not guarantee recovery of events already dropped.
