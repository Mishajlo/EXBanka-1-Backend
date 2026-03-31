# GitHub Workflows Refactor Design

## Goal

Split the single `Build&Publish_Docker_Images.yml` workflow into two focused files (build-only and build+push), fix the `notification-service` Docker build failure caused by a missing `go.sum` entry, and remove the stale XML warning on integration tests.

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
- **Job guard**: `if: github.repository == 'RAF-SI-2025/EXBanka-1-Backend'` (same as existing file — prevents fork builds from failing on missing registry credentials)
- **Job name**: `Build ${{ matrix.service }}`
- **Runner**: `ubuntu-latest`
- **Permissions**: `contents: read` only (no `packages: write` — no push)
- **Strategy**: `fail-fast: false` (one service failure does not cancel others)
- **Matrix** (11 services):
  ```
  api-gateway, auth-service, user-service, notification-service,
  client-service, account-service, card-service, transaction-service,
  credit-service, exchange-service, seeder
  ```
- **Workflow-level env vars**:
  ```yaml
  env:
    REGISTRY: ghcr.io
    IMAGE_PREFIX: ghcr.io/raf-si-2025
  ```
- **Steps** (in order):
  1. `actions/checkout@v4`
  2. `docker/setup-qemu-action@v3` (required for multi-platform builds)
  3. `docker/setup-buildx-action@v3` (required for `cache-to: type=gha`)
  4. `docker/build-push-action@v6` with:
     - `context: .`
     - `file: ${{ matrix.service }}/Dockerfile`
     - `platforms: linux/amd64,linux/arm64`
     - `push: false`
     - `tags: ${{ env.IMAGE_PREFIX }}/${{ matrix.service }}:${{ github.sha }}`
     - `cache-from: type=gha,scope=${{ matrix.service }}`
     - `cache-to: type=gha,mode=max,scope=${{ matrix.service }}`
- **No `docker/login-action` step** — no registry auth needed when not pushing.

### `docker-publish.yml` (new)

Replaces the push-on-merge half of `Build&Publish_Docker_Images.yml`.

- **Trigger**: `push` targeting `Development`
- **Job guard**: `if: github.repository == 'RAF-SI-2025/EXBanka-1-Backend'`
- **Job name**: `Build ${{ matrix.service }}`
- **Runner**: `ubuntu-latest`
- **Permissions**: `contents: read`, `packages: write`
- **Strategy**: `fail-fast: false`
- **Matrix**: same 11 services as `docker-build.yml`
- **Workflow-level env vars**:
  ```yaml
  env:
    REGISTRY: ghcr.io
    IMAGE_PREFIX: ghcr.io/raf-si-2025
  ```
- **Steps** (in order):
  1. `actions/checkout@v4`
  2. `docker/login-action@v3` with `registry: ${{ env.REGISTRY }}`, `username: ${{ github.actor }}`, `password: ${{ secrets.GITHUB_TOKEN }}`
  3. `docker/setup-qemu-action@v3`
  4. `docker/setup-buildx-action@v3`
  5. `docker/build-push-action@v6` with:
     - `context: .`
     - `file: ${{ matrix.service }}/Dockerfile`
     - `platforms: linux/amd64,linux/arm64`
     - `push: true`
     - `tags:` both `:${{ github.sha }}` and `:latest` under `${{ env.IMAGE_PREFIX }}/${{ matrix.service }}`
     - `cache-from: type=gha,scope=${{ matrix.service }}`
     - `cache-to: type=gha,mode=max,scope=${{ matrix.service }}`

### `integration-tests.yml` (unchanged)

The primary cause of the missing XML file is the `notification-service` Docker build failure (see Bug Fix section below). Once that is fixed, `docker compose up --build -d --wait` succeeds, `gotestsum` runs, and writes `test-results.xml`. The upload step already has `if: always()` which ensures it runs even when tests fail. No changes needed to this file.

### `publish-test-results.yml` (unchanged)

The existing implementation is correct: `workflow_run` trigger, `actions/download-artifact@v4` with `run-id: ${{ github.event.workflow_run.id }}`, `dorny/test-reporter@v1`. No changes needed.

## Bug Fix: `notification-service` go.sum

**Root cause**: `contract/shared/optimistic_lock.go` imports `gorm.io/gorm`. Under the Go workspace (`go.work`), this resolves via the workspace graph. The `notification-service` Dockerfile sets `GOWORK=off` and runs `go mod download` in isolation. `notification-service/go.mod` does not list `gorm.io/gorm` as a dependency, so `go.sum` has no entry for it. Build fails with:
```
missing go.sum entry for module providing package gorm.io/gorm
(imported by github.com/exbanka/contract/shared)
```

**Fix**: Run `go mod tidy` inside the `notification-service/` directory while the `replace github.com/exbanka/contract => ../contract` directive is active (it already is in `notification-service/go.mod`). This adds `gorm.io/gorm` as an indirect dependency to `notification-service/go.mod` and adds the required checksums to `notification-service/go.sum`.

```bash
cd notification-service && go mod tidy
```

This is a one-time code fix. No Dockerfile changes, no workflow changes.

**Cascade effect**: Once the Docker build succeeds, `docker compose up --build -d --wait` succeeds, `gotestsum` runs and writes `test-results.xml`, and the artifact upload finds the file. The "no XML file" warning is a downstream symptom of this bug.

## Files changed

| File | Change type | Purpose |
|------|------------|---------|
| `.github/workflows/Build&Publish_Docker_Images.yml` | Delete | Replaced by two focused files |
| `.github/workflows/docker-build.yml` | Create | Build-only on PRs, populates GHA cache |
| `.github/workflows/docker-publish.yml` | Create | Build+push on merge, reads GHA cache |
| `notification-service/go.mod` | Modify | Add `gorm.io/gorm` as indirect dependency |
| `notification-service/go.sum` | Modify | Add checksums for `gorm.io/gorm` and its deps |

## Out of scope

- Adding a unit test workflow (`make test`) — not requested
- Changing integration test triggers or test content
- Modifying other Dockerfiles (other services do not have the same go.sum gap)
- Adding `workflow_run` dependency between `docker-build` and `docker-publish` (not needed; triggers are mutually exclusive: PR vs push)
