# container-scheduler

A minimal Go scheduler for Libation: start an existing container every four hours,
leave it to finish, and skip starts while it is running. This project is not a
full Ofelia replacement. The Go module is `github.com/jacobfg/container-scheduler`;
the executable and local image are named `container-scheduler`.

## Build and check

Requires Go 1.27.3 or newer.

```sh
go test -race ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/container-scheduler-linux-amd64 ./cmd/container-scheduler
docker build -t container-scheduler:local .
```

The runtime image is non-root, contains the static binary and CA certificates,
and needs only the private tmpfs for its control socket. Docker integration runs in GitHub Actions using `python3 scripts/smoke.py`.
It uses isolated container names and exercises the restricted proxy.

## Configuration

| Environment | Value |
| --- | --- |
| `CONTAINER_LABEL_SELECTOR` | Required single `key=value` Docker discovery filter |
| `TZ` | Log timezone, e.g. `Australia/Sydney`; defaults to UTC in the image |
| `LABEL_PREFIX` | `ofelia` by default; selects one label namespace |
| `DOCKER_HOST` | Docker SDK default Unix socket, or `tcp://scheduler-socket-proxy:2375` |
| `DOCKER_API_TIMEOUT` | `15s`; positive duration, total bound per scheduled inspect/start operation |

Docker SDK environment settings also support TLS and API version overrides.
API negotiation is enabled by the SDK by default. JSON logs go to stdout.
Timezone data is embedded in the binary, so IANA names work in the scratch image
without host mounts. Log timestamps and next-run timestamps honour `TZ`, including
daylight saving. Invalid timezone names fall back to UTC. Interval scheduling
remains elapsed-time based; changing `TZ` does not change the four-hour interval.

Place these labels on a container matching the discovery selector (the scheduler
itself in the example):

```yaml
container-scheduler.scope: libation
ofelia.enabled: 'true'
ofelia.service: 'true'
ofelia.job-run.libation.schedule: '@every 4h'
ofelia.job-run.libation.container: libation
ofelia.job-run.libation.no-overlap: 'true'
ofelia.job-run.libation.delete: 'false'
```

All four job fields are required. `service` is accepted for compatibility and
has no behavior. Disabled/unmarked containers are ignored. Enabled containers
with unsupported labels, invalid jobs, duplicate target containers, or an empty
job set cause a nonzero startup exit. Only positive Go durations after `@every `
are accepted (for example `4h`, `30m`, `1h30m`; not `1d`). Cron and job-exec are
not supported.

Discovery occurs once at startup. Restart the scheduler after changing labels.
The first tick occurs one full interval after discovery. Restarting resets that
interval; downtime is not replayed. Elapsed ticks are discarded, with subsequent
ticks aligned to the original interval. This is interval scheduling, not six
fixed local-clock times per calendar day.

Every tick inspects the target by name and starts it by name if stopped. Running,
restarting, or paused containers are skipped. Missing containers and Docker API
failures are logged and retried at the next interval. Startup discovery failures
exit nonzero so the deployment restart policy retries. A start timeout can mean
Docker accepted the request; the next tick inspects state again. No container
output, exit status, deletion, exec, stop, or creation is managed here.

Run one scheduler instance and avoid other schedulers managing the same target.
Inspect/start is not a distributed lock. SIGTERM cancels API requests and timers
without stopping target containers. The scheduler does not report job completion.

## Run now and reset the interval

Trigger a named job using the CLI inside the running scheduler container:

```sh
docker compose exec -T scheduler /container-scheduler run-now libation
# Explicitly trigger every configured job:
docker compose exec -T scheduler /container-scheduler run-now --all
```

The name comes from `ofelia.job-run.<name>.*`, not the target container name.
Job names must be unique across discovered containers, start with an alphanumeric
character, and contain only alphanumerics, underscores or hyphens (dots separate
label fields). Duplicate job names fail discovery.

The command queues an immediate attempt for only the selected job and resets
that job's next attempt to one full interval after the request is handled (four
hours for Libation). Other jobs keep their timers. Running containers are still
skipped; a skip or API error also resets the selected timer. There is at most one
pending request per job; repeats while pending are coalesced. Requests wait for
any in-flight Docker operation, bounded by its timeout.

A successful CLI exit means accepted, not that Docker successfully started the
container. Logs record `manual run requested; timer reset` with the new `next`
timestamp, then a start, skip, or error message. Unknown job names, invalid
arguments, or an unavailable scheduler produce a nonzero CLI exit.

Control uses a Unix socket at `/run/container-scheduler/control.sock`, mode 0600;
only the scheduler user or root can access it. No network port or additional
Docker API permissions are used. The example Compose module supplies a 1 MiB
private tmpfs at `/run/container-scheduler`, owned by UID/GID 65532, mode 0700.
Add that mount when upgrading an existing read-only deployment. `SCHEDULER_SOCKET`
can override the socket path for both daemon and CLI; its parent must be private
and writable by the scheduler. The socket is removed on shutdown; stale sockets
are recovered after a crash, while active sockets and regular files are protected.

Versions 0.1.0 and 0.1.1 do not support run-now. There is no SIGUSR1 interface.

## Healthcheck

The image healthcheck runs `/container-scheduler healthcheck` every 30 seconds.
It sends a read-only request through the same Unix socket, with a three-second
client timeout. You can also run it manually:

```sh
docker compose exec -T scheduler /container-scheduler healthcheck
```

Healthy means job discovery completed and the control endpoint responds with at
least one loaded job. It does not start jobs, reset timers, contact Docker, or
claim that past jobs succeeded. Docker outages are logged by scheduled attempts
and do not make the control healthcheck fail. This is a liveness check, not a
per-job progress watchdog. An unhealthy Docker status alone does not restart a
container under `restart: unless-stopped`.

## Compose integration

`compose/scheduler.yml` is an example module, not a live migration. Include it
from the root Compose project and declare:

```yaml
networks:
  scheduler_socket_proxy:
    internal: true
```

Build `container-scheduler:local` on the Docker host first. Preserve Libation's
existing `scheduled` profile, `restart: "no"`, `SLEEP_TIME=-1`, user, mounts, and
resource limits. Prepare it without running it before starting the scheduler:

```sh
docker compose --profile scheduled up --no-start --no-deps libation
docker compose up -d scheduler-socket-proxy scheduler
```

The proxy permits listing containers, inspecting Libation, negotiating the API,
and starting only `libation`. It denies exec/create/stop/delete and log streaming.
The discovery selector controls job selection; the proxy enforces write access.
`:ro` on the host socket does not itself restrict Docker API writes.
The example retains the existing pinned proxy image. Memory ceilings are initial
values to validate on the host, not measured production requirements.

For the later homeserver migration, stop the old Ofelia instance before enabling
this scheduler. Update deployment helpers to pause `scheduler` before checking
Libation and restore only a previously running scheduler on failure. On the
first migration also stop legacy Ofelia explicitly: the new Compose model will
no longer know its service. Defer migration if Libation is running. Remove legacy
services only after successful handover. The homeserver repo is unchanged here.

## Manual Docker smoke tests

Use a disposable Docker host/project with a test container named `libation`;
do not run these tests against the real library.

1. Create (do not start) an Alpine container named `libation` whose command prints
   a marker, sleeps 15 seconds, then exits, with restart disabled.
2. Include the example module in a temporary root Compose project and declare
   its internal network. Override the scheduler's schedule label to `@every 10s`.
3. Build the image and start proxy and scheduler. Confirm no immediate start,
   a start after 10 seconds, a skip while running, and another start after exit.
4. Stop the scheduler during the sleep. Confirm the target finishes naturally.
   Restart the scheduler and confirm the full initial delay returns.
5. Between ticks, remove the stopped test target and create another container
   with the same name. Confirm it is discovered by name on the next tick.
6. Stop the proxy for a tick, then restore it. Confirm an error is logged and a
   later tick recovers. A missing target should likewise recover once recreated.
7. Verify proxy logs show no denied legitimate requests and attempt forbidden
   create/exec/delete/start-other-container requests through the proxy; expect denial.
8. Restore `@every 4h` for deployment. Observe memory and restart counts through
   several real cycles before treating the image as production-ready.

## Scheduled dependency updates

The Renovate Bot workflow follows the homeserver-docker setup: a self-hosted
Action using `.github/renovate.json5`, SHA-pinned actions, and `RENOVATE_TOKEN`.
It runs every six hours at minute four (UTC), and can also be run from Actions
using `workflow_dispatch`.
It groups stable minor and patch updates to Go, the Dockerfile build image,
and direct and indirect Go modules into one application maintenance PR. It
updates the README Go requirement and runs `go mod tidy` to maintain `go.sum`.
An existing open maintenance PR is refreshed; a run with no updates creates
no PR. Major upgrades remain manual. Merging remains manual too.

Before enabling the schedule, add an Actions repository secret named
`RENOVATE_TOKEN` with a dedicated GitHub PAT authorized for this repository
(see [Renovate authentication](https://docs.renovatebot.com/modules/platform/github/)).
Use a token with contents and pull-request write access, and the read permissions
Renovate requires. Do not use the workflow's default `GITHUB_TOKEN`: the dedicated
token lets Renovate's pushes and PRs trigger the existing test workflow.
For a fine-grained PAT, explicitly include `jacobfg/container-scheduler` in
its repository access list; a token restricted to `homeserver-docker` cannot
access this repository. The workflow checks repository access before starting
Renovate. For troubleshooting, enable the `debug` input on a manual run to
include Renovate's underlying API errors in the log. The schedule
starts once the workflow is merged into the default branch.

CI checks that `go.mod`, the Dockerfile, and this README name the same exact Go
release, then runs the application checks below. A release published before its
Docker image is available may temporarily fail alignment; review or rerun the
maintenance update once both sources are available. Use the `upgrade-app` skill
for compatibility fixes or major upgrades that need source changes.

Renovate is scoped to application dependencies; Compose deployment images and
GitHub Actions are not updated by this configuration. The Renovate action and
engine versions in `.github/workflows/renovatebot.yml` are pinned and maintained
manually.

## CI and tag-only releases

Branch pushes and pull requests run formatting/module checks, shuffled race tests
(repeated ten times), vet, amd64/arm64 image builds, and a real Docker smoke test.
The smoke test checks delayed starts, overlap skips, shutdown, container recreation,
proxy outage recovery, denied API writes, non-root execution, and restart count.

Publishing runs **only on pushed stable version tags**, such as `0.1.0` (no `v`
prefix), matching Fittools. The tagged commit must be an ancestor of `main` and
must pass the complete reusable test workflow before GHCR login or publication.
Branch pushes, PRs, and manual dispatch cannot publish. No release tag is created
automatically.

```sh
git tag 0.1.0
git push origin 0.1.0
```

Successful releases publish `ghcr.io/jacobfg/container-scheduler:0.1.0` and `:latest`
for Linux amd64 and arm64, with OCI source, revision, and version labels. Like
Fittools, every successful release updates `latest`, including an older version
if it is tagged later. Use version/digest pins in production.

Full Ofelia compatibility remains outside the initial scope.
