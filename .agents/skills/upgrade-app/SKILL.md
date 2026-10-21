---
name: upgrade-app
description: Upgrade this container-scheduler repository's Go toolchain and Go modules, patch compatibility issues, run its checks, and create a pull request. Use for application maintenance and dependency upgrade requests.
---

# Upgrade container-scheduler

Complete the upgrade through a pull request. Read the repository's `AGENTS.md`,
`go.mod`, `Dockerfile`, README build instructions, and current test workflow first.

## Select versions and prepare

- Inspect git status, branch, remotes, and existing PRs. Preserve unrelated work;
  use a separate worktree if necessary. Base a new upgrade branch on the remote
  default branch unless the user specifies another base. Reuse an existing
  upgrade branch/PR when continuing the same task.
- Use versions requested by the user. Otherwise verify the latest stable Go
  release using `https://go.dev/dl/?mode=json` and confirm its exact
  `golang:X.Y.Z` Docker image tag exists. Do not guess versions, use prereleases,
  or downgrade an existing version silently. If the sources cannot be verified,
  report the blocker instead of inventing a target.
- Use the selected Go toolchain for module resolution and validation. Confirm
  with `go version`; a `go` directive alone does not prove which toolchain ran.

## Patch the application

1. Update the `go` directive in `go.mod` and the Dockerfile's `golang:X.Y.Z` build
   image to the same exact release. Preserve the platform arguments and runtime
   image. Update the README Go requirement. If a `toolchain` directive exists,
   keep it consistent; do not add one unnecessarily. CI reads `go.mod` via
   `go-version-file`, so it normally needs no separate version edit.
2. Upgrade modules with `go get -u ./...`, then `go mod tidy`. For an explicitly
   patch-only request, use `go get -u=patch ./...` instead. Respect any requested
   package/version limits. Review `go list -m -u all` and the resulting diff for
   remaining upgrades, prereleases, changed minimum Go versions, or unexpected
   replacements. Do not blindly pin every transitive dependency or introduce
   major module-path migrations unrelated to the maintenance request.
3. Patch source for dependency API changes while preserving scheduler semantics,
   Docker API negotiation, control socket behavior, and proxy restrictions.
   Add or adjust tests for behavior affected by compatibility fixes. Format
   changed Go files, retain both `go.mod` and `go.sum`, and recheck Go version
   alignment after tidy.

## Validate

Follow the current `.github/workflows/test.yml`; at minimum run:

```sh
go mod verify
go test -race -shuffle=on -count=10 -coverprofile=coverage.out ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/container-scheduler-linux-amd64 ./cmd/container-scheduler
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bin/container-scheduler-linux-arm64 ./cmd/container-scheduler
docker build -t container-scheduler:local .
python3 scripts/smoke.py
```

Check `gofmt -l cmd internal` produces no paths. Run `go mod tidy` again and
confirm it makes no further changes. Use CI for the two-architecture image build
and any checks unavailable locally. The smoke script requires Docker Compose
and creates isolated resources; never run manual fixtures against real Libation.
Fix upgrade-related failures and rerun affected checks. Do not weaken tests or
proxy permissions to obtain a passing result.

## Create the PR

- Review `git diff --check` and the complete diff. Stage only upgrade files,
  excluding binaries and coverage output, then commit and push the upgrade
  branch. The upgrade request includes creating the PR; proceed within existing
  execution permissions without adding a separate confirmation step.
- Create a PR with `gh pr create` (or an available GitHub integration), using the
  actual repository and base branch. Write a multiline body to a temporary file
  and pass `--body-file`. Include old/new Go and direct module versions, notable
  compatibility changes, and precise local validation results and limitations.
- If local checks are unavailable, create a draft PR and let CI perform them.
  Watch checks with `gh pr checks --watch`, inspect failures, and fix failures
  attributable to the upgrade. Mark the draft ready when required checks pass.
  If blocked by credentials, infrastructure, or an unresolved failure, retain
  the reviewable changes and report the blocker; do not claim completion.
- Reuse/update the existing PR on retries rather than creating duplicates. If
  versions are already current and there is no diff, report that no PR is needed.
- Return the PR link, version changes, and test/CI outcome. Do not merge the PR,
  create release tags, publish images, or deploy as part of this skill.
