# Review workspace container acceptance

The opt-in `CI` workflow input `review_workspace_acceptance` builds `sandbox/Dockerfile` from the dispatched revision, installs gVisor with the supported worker flags, and runs the production Docker provider against that image. Dispatch the complete stack's feature branch: the cold path must include workspace lifecycle integration. Workflow dispatch does not deploy that branch.

The job fails when Docker, the explicit image, the scoped network, Linux, or `runsc` is unavailable. It never substitutes mocked exec, host Git execution, or the standard `runc` runtime for container acceptance. Ordinary Go suites skip the container suite; a requested Make target cannot skip it.

```sh
docker build --label "org.opencontainers.image.revision=$(git rev-parse HEAD)" -t review-acceptance:local -f sandbox/Dockerfile .
docker network create review-acceptance-local
REVIEW_ACCEPTANCE_IMAGE=review-acceptance:local \
REVIEW_ACCEPTANCE_NETWORK=review-acceptance-local \
REVIEW_ACCEPTANCE_RUNTIME=runsc \
REVIEW_ACCEPTANCE_RUNSC_VERSION="$(runsc --version | head -n 1)" \
REVIEW_ACCEPTANCE_SOURCE_SHA="$(git rev-parse HEAD)" \
REVIEW_ACCEPTANCE_ARTIFACT_DIR="$(mktemp -d)" \
make test-review-container-acceptance
```

The runtime must already be installed and registered. On a disposable Linux host, use the package source and `runsc install -- --ignore-cgroups --host-uds=open` invocation from `deploy/scripts/bootstrap.sh`. The hosted job performs that setup and removes only containers attached to its own network.

The acceptance cases use isolated synthetic repositories with divergent captured base/head/merge-base commits, filtered file transport, actual offline Go vendoring, and actual locked npm installation. They exercise:

- Cold materialization with local reads of all captured trees and a version 2 evidence receipt.
- Warm reuse without installation or token lookup.
- A real `DockerProvider.Snapshot` followed by destruction, creation, and `RestoreForReviewPreparation`; source, dependency roots, and receipt survive that transport.
- The original eight-minute preparation budget, with queue time leaving a short remainder. All stages retain the same absolute deadline and the recipe process group stops before container destruction.
- Expired preparation with bounded platform-only verification: no setup, arbitrary repository verifier, token lookup, or receipt replacement.
- Rejection and cleanup for unsafe Git configuration, present packed-object corruption, and an explicit recipe failure.
- Absence of a synthetic private input from inspected creation environments, actual Docker exec argv/environments, provider logs, Git configuration, receipts, and Linux recipe process ancestry.

Before container destruction, the deadline case records the recipe PID and every observed member of its process group from Linux `/proc`. An absent process or an observed terminal `Z`/`X` state counts as stopped; every live or unknown state fails. This distinguishes stopped children retained as zombies by container PID 1 from running children.

The job uploads bounded per-scenario JSON and logs even when a scenario fails. JSON identifies the source SHA, image ID, operating system, architecture, Docker and gVisor versions, runtime, resource limits, elapsed times, token counts, receipt hash, and cleanup observations. The fixture-building test runs separately during ordinary host verification; its success is not container acceptance.

File transport does not prove live HTTPS authentication. Synthetic dependency and source fixtures do not accept a production repository recipe or establish the largest repository's hashing cost. Disk quotas, deployed image identity, worker network policy, and deployment are separate rollout evidence. Keep exact repository inventory and rollout evidence in the private infrastructure checkout.

The `cleanup_signal_failure` case injects a synthetic TERM permission error only for the recorded long-running recipe group. It requires the real KILL attempt and terminal cleanup guidance while preserving the original preparation deadline, then verifies a regular container-local quarantine marker and no live group member before destruction. This checks error handling in the actual runtime; natural Linux permission-denial reachability is not asserted.
