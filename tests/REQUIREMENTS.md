# Testing Plan (pre-implementation)

End-to-end testing strategy for the REST API defined in `docs/server_api.yaml`, aligned with constraints in `docs/SPEC.md`. Designed to exist before the server implementation; tests start as failing but compile, then turn green as features land.

## Goals
- Validate functional correctness for CRUD + SCAN, idempotency, last-write-wins, and durability (WAL/replay).
- Cover negative cases (malformed base64, missing bounds, 404 semantics).
- Exercise performance/latency targets under mixed workloads with reproducible inputs.

## Dependencies
- Generated Go client from `docs/server_api.yaml` (e.g., `oapi-codegen`) in `client/`.
- Server binary launched by tests with configurable data dir and port; until available, use `httptest.Server` stubs returning 501 to keep harness compiling.

## Functional E2E (Go, `tests/e2e`)
- Lifecycle tests: PUT→GET round-trip, overwrite with newer value, DELETE tombstone then GET/SCAN reflects deletion.
- Range scans: inclusive `fromKey`, exclusive `endKey`, empty range, large range pagination once implemented.
- Idempotency: repeat PUT/DELETE on same key; ensure stable results.
- Crash/restart: write workload, send SIGKILL to server, restart on same data dir, verify WAL replay restores last state.
- Concurrency: concurrent PUT/DELETE on same key; assert last-write-wins ordering via sequence/causal timestamps from client harness.
- Negative: malformed base64, missing query params, unknown key (404), oversized payload (when enforced).

## Performance/Load (k6 or vegeta, `tests/perf`)
- Workloads: read-heavy, write-heavy, mixed (e.g., 50/50), scan-heavy. Parameterize key distribution (uniform/Zipfian), value sizes, and key cardinality.
- Measurements: P50/P95/P99 latency, throughput, error rates. Record compaction backlog and fsync counts once metrics exist.
- Durability stress: writes with fsync-every-batch vs grouped fsync; measure tail latency impact.
- Long soak: sustained load with periodic server restart to validate recovery while under churn.

## Fixtures & helpers
- Small deterministic fixtures for correctness; synthetic generators for larger perf datasets with fixed seeds.
- Helper to spin server: build binary, start with temp data dir/port, wait for health endpoint, tear down with context timeout.
- Log/metrics capture: store server logs and `/metrics` snapshot alongside test artifacts for triage.

## CI strategy
- `make test-e2e`: functional suite; runs per PR.
- `make test-perf`: load tests; run on-demand or nightly to avoid noisy CI. Thresholds configurable via env (SLO guardrails).

## Acceptance checkpoints
- Functional: all CRUD/SCAN/idempotency/crash-restart tests pass against the binary.
- Perf: meets defined latency/error thresholds under chosen workload mix with fsync-safe settings.
