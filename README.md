# Simple Key-Value Store (LSM-based)

Project layout and conventions for the Go module:

- `cmd/server/` — main entrypoint; minimal wiring to start the HTTP server (env/config → run).
- `internal/api/` — REST transport layer generated from `docs/server_api.yaml`; thin handlers and routing only.
- `internal/engine/` — core database logic: write path, memtable, WAL, SSTable orchestration, compaction, tombstones, sequencing.
- `internal/storage/` — filesystem and format concerns: WAL I/O, SSTable I/O, checksums, block cache, format versions.
- `internal/replication/` (future) — clustering, token ring/consistent hashing, gossip, quorum math.
- `internal/metrics/` (or `pkg/metrics` if reused) — Prometheus/observability hooks and logging adapters.
- `pkg/...` — only for code intended to be imported externally; keep most implementation in `internal/` to avoid API leakage.
- `tests/e2e/` — end-to-end harness and generated client; uses `scripts/gen-e2e-client.sh` to stay in sync with the OpenAPI spec.
- `docs/` — specs and design docs (e.g., `server_api.yaml`, `SPEC.md`).
- `third_party/` — minimal vendored helpers (e.g., stub runtime for codegen) to avoid network requirements; remove when upstream deps are fetched normally.
- `scripts/` — tooling (client generation, etc.).

Notes:
- No `src/` wrapper; Go modules root the code tree.
- Expose only stable APIs via `pkg/`; keep the rest under `internal/` to iterate freely.

## Functional & durability constraints (from `docs/SPEC.md`)
- Arbitrary binary keys/values; clients handle base64/url-safe encoding on transport.
- Operations: GET (read), PUT (idempotent create/update), DELETE (tombstone), SCAN (inclusive `fromKey`, exclusive `endKey`).
- Durability path: a write is acknowledged only after it lands in both memtable and commit log; memtables flush to disk on size thresholds.
- Commit log: buffered, flushed on threshold or interval; tracks flushed mutations to archive segments once SSTables persist them.
- Crash recovery: on restart, replay commit logs to rebuild memtables; last-write-wins semantics across updates.
- Connectivity: REST API implements the database contract defined in `docs/server_api.yaml`.

## Code generation
- REST server stubs: `./scripts/gen-api-server.sh` (or `go generate ./internal/api`) regenerates `internal/api/generated.go` from `docs/server_api.yaml` using `oapi-codegen` (install with `go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.3.0`).
- E2E client: `./scripts/gen-e2e-client.sh` (or `go generate ./tests/e2e`) regenerates `tests/e2e/apiclient/generated.go` from the same spec.

## Build & run
- Build all packages (offline-friendly if cache dirs are redirected): `env GOCACHE=$(pwd)/.cache GOMODCACHE=$(pwd)/.cache/gomod go build ./...`
- Run the HTTP server: `go run ./cmd/server` (override address with `KV_HTTP_ADDR=127.0.0.1:9090`)
- Health endpoint: `curl http://127.0.0.1:8080/health`

## Pre-commit hooks
- Config lives in `.pre-commit-config.yaml` (format, imports, spelling, lint/vet, secrets).
- Install pre-commit tooling: `pip install pre-commit` (or your package manager).
- Install hook: `pre-commit install`
- Run on demand: `pre-commit run --all-files`
- Required tools: `gofmt` (std), `goimports`, `misspell`, `gitleaks`, and optionally `golangci-lint` (falls back to `go vet`). Install helpers once:
  - `go install golang.org/x/tools/cmd/goimports@latest`
  - `go install github.com/client9/misspell/cmd/misspell@latest`
  - `go install github.com/zricethezav/gitleaks/v8@v8.18.1` (earlier version compatible with Go 1.21; module path uses `zricethezav`)
  - `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest` (optional; otherwise `go vet` runs)
