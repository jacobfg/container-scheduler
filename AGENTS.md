# Repository guidance

This repository builds `container-scheduler`, a small Go service that starts
existing Docker containers on intervals. Read `README.md` for supported behavior
and configuration before changing it.

## Layout and boundaries

- `cmd/container-scheduler/`: daemon entrypoint, configuration, Unix control
  socket, `run-now`, and `healthcheck` commands.
- `internal/scheduler/`: label parsing, interval scheduling, and Docker adapter.
- `compose/scheduler.yml`: example deployment and restricted Docker proxy.
- `scripts/smoke.py`: isolated Docker integration test.
- `.github/workflows/test.yml`: authoritative CI checks.
- `.github/workflows/publish.yml`: stable-tag-only image publication.

Preserve delayed first runs, overlap skipping, cancellation, and manual timer
resets. The scheduler starts existing containers; it does not create, stop,
delete, or exec into target containers. Healthchecks must remain read-only.
Keep the static, non-root scratch image and restricted proxy permissions.
Deployment examples do not authorize changes to a live Libation installation.

## Validation

Format changed Go code with `gofmt`. For Go or dependency changes, run:

```sh
go mod tidy
go test -race -shuffle=on -count=10 -coverprofile=coverage.out ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/container-scheduler-linux-amd64 ./cmd/container-scheduler
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bin/container-scheduler-linux-arm64 ./cmd/container-scheduler
docker build -t container-scheduler:local .
python3 scripts/smoke.py
```

The smoke test requires Docker with Compose and uses generated container names.
Use an isolated Docker environment; do not adapt manual tests to target the real
library. CI also builds both image architectures. Report unavailable checks
explicitly and use CI to complete them. Documentation-only changes need review
of paths and instructions, not the application test suite.

## Upgrades and pull requests

For Go and module upgrades, follow
[the upgrade-app skill](.agents/skills/upgrade-app/SKILL.md).
Keep the Dockerfile Go image, `go.mod` directive, and README requirement aligned.
Commit `go.sum` changes with dependency updates. Make only compatibility fixes
needed by the upgrade and add regression tests for changed behavior.

PRs should explain the change, version transitions, and validation results.
Preserve unrelated working-tree changes. Creating an upgrade PR does not include
merging, deployment, or release tagging: pushing a stable tag publishes images.
