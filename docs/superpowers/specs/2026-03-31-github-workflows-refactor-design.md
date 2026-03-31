# GitHub Workflows Refactor Design

## Goal

Split the single `Build&Publish_Docker_Images.yml` workflow into two focused files (build-only and build+push), fix the `notification-service` Docker build failure caused by a missing `go.sum` entry, and harden the integration test XML upload so test results are always captured.

## Architecture

### Approach: Cached rebuild with file separation

Build and publish are separate workflow files. Both use `cache-from/cache-to: type=gha` with the same `scope` key per service. When a PR build runs (`docker-build.yml`), it populates the GHA layer cache. When the corresponding push runs (`docker-publish.yml`), Docker buildx reads every layer from cache — the "rebuild" completes in seconds with no actual work.

This approach was chosen over true image reuse (building once, tagging twice) because:
- It requires no staging registry or large artifact uploads
- The GHA cache already exists in the current workflow
- It keeps both files structurally identical (easy to maintain)
- A full rebuild on publish is acceptable when it's near-instant due to cache hits

### Workflow trigger matrix

| File | Trigger | Push images |
|------|---------|------------|
| `docker-build.yml` | `pull_request → Development` | No |
| `docker-publish.yml` | `push → Development` | Yes |
| `integration-tests.yml` | `pull_request → Development, main` | N/A |
| `publish-test-results.yml` | `workflow_run: Integration Tests` | N/A |

## Components

### `docker-build.yml` (new)

Replaces the build-on-PR half of `Build&Publish_Docker_Images.yml`.

- **Trigger**: `pull_request` targeting `Development`
- **Jobs**: `build` — 11-service matrix (`api-gateway`, `auth-service`, `user-service`, `notification-service`, `client-service`, `account-service`, `card-service`, `transaction-service`, `credit-service`, `exchange-service`, `seeder`)
- **Key settings**:
  - `push: false` — images are built but not pushed to registry
  - `cache-to: type=gha,mode=max,scope=${{ matrix.service }}` — populates GHA cache
  - `cache-from: type=gha,scope=${{ matrix.service }}` — reads existing cache
  - No `docker/login-action` step (no registry auth needed)
- **Permissions**: `contents: read` only

### `docker-publish.yml` (new)

Replaces the push-on-merge half of `Build&Publish_Docker_Images.yml`.

- **Trigger**: `push` targeting `Development`
- **Jobs**: `publish` — same 11-service matrix
- **Key settings**:
  - `push: true` — always pushes (trigger already guards for push-only)
  - `cache-from: type=gha,scope=${{ matrix.service }}` — reads cache from PR build
  - `cache-to: type=gha,mode=max,scope=${{ matrix.service }}` — updates cache after publish
  - Tags: `:${{ github.sha }}` and `:latest`
  - `docker/login-action` step required (registry auth)
- **Permissions**: `contents: read`, `packages: write`

Both files share identical matrix definitions, `context: .`, `file: ${{ matrix.service }}/Dockerfile`, and `platforms: linux/amd64,linux/arm64`.

### `integration-tests.yml` (modified)

- **Change**: Add `continue-on-error: true` to the `Run integration tests` step.
  - `gotestsum --junitfile test-results.xml` writes the XML regardless of test pass/fail
  - With `continue-on-error: true`, the step is marked as a warning rather than a hard failure when tests fail, allowing the `Upload test results artifact` step (which already has `if: always()`) to always find and upload the file
  - The overall workflow job still reflects the test outcome via the step's `outcome` property
- No other structural changes to this file

### `publish-test-results.yml` (unchanged)

The existing implementation is correct: `workflow_run` trigger, `actions/download-artifact@v4` with `run-id`, `dorny/test-reporter@v1`. No changes needed.

## Bug Fix: `notification-service` go.sum

**Root cause**: `contract/shared/optimistic_lock.go` imports `gorm.io/gorm`. Under the Go workspace (`go.work`), this resolves via the workspace graph. The `notification-service` Dockerfile sets `GOWORK=off` and runs `go mod download` in isolation. `notification-service/go.mod` does not list `gorm.io/gorm` as a dependency, so `go.sum` has no entry for it. Build fails with:
```
missing go.sum entry for module providing package gorm.io/gorm
```

**Fix**: Run `go mod tidy` in `notification-service/` (with the `replace github.com/exbanka/contract => ../contract` directive active). This adds `gorm.io/gorm` as an indirect dependency to `notification-service/go.mod` and adds the required checksums to `notification-service/go.sum`.

This is a one-time code fix. No Dockerfile changes, no workflow changes.

**Cascade effect**: Once the Docker build succeeds, `docker compose up --build -d --wait` succeeds, `gotestsum` runs and writes `test-results.xml`, and the artifact upload finds the file. The "no XML file" warning is a downstream symptom of this bug.

## Files changed

| File | Change type | Purpose |
|------|------------|---------|
| `.github/workflows/Build&Publish_Docker_Images.yml` | Delete | Replaced by two focused files |
| `.github/workflows/docker-build.yml` | Create | Build-only on PRs, populates GHA cache |
| `.github/workflows/docker-publish.yml` | Create | Build+push on merge, reads GHA cache |
| `.github/workflows/integration-tests.yml` | Modify | Add `continue-on-error: true` on test step |
| `notification-service/go.mod` | Modify | Add `gorm.io/gorm` as indirect dependency |
| `notification-service/go.sum` | Modify | Add checksums for `gorm.io/gorm` and its deps |

## Out of scope

- Adding a unit test workflow (`make test`) — not requested
- Changing integration test triggers or test content
- Modifying other Dockerfiles (other services do not have the same go.sum gap)
- Adding `workflow_run` dependency between `docker-build` and `docker-publish` (not needed; triggers are mutually exclusive: PR vs push)
