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

## Code generation
- REST server stubs: `./scripts/gen-api-server.sh` (or `go generate ./internal/api`) regenerates `internal/api/generated.go` from `docs/server_api.yaml` using `oapi-codegen` (install with `go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.3.0`).
- E2E client: `./scripts/gen-e2e-client.sh` (or `go generate ./tests/e2e`) regenerates `tests/e2e/apiclient/generated.go` from the same spec.
