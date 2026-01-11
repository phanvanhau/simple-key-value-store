# E2E Test Harness

This directory hosts the end-to-end tests for the REST API defined in `../docs/server_api.yaml`. Tests can run against:
- a live server you start yourself (`KV_SERVER_URL`), or
- a server launched by the test harness (`KV_SERVER_CMD`), or
- a stub server that returns 501 (default; tests will fail) when no server is provided.

## Running
- Against an existing server:
  ```sh
  KV_SERVER_URL=http://localhost:8080 go test ./tests/e2e
  ```
- Let the harness launch the server (must listen on `KV_HTTP_ADDR` and store data in `KV_DATA_DIR`):
  ```sh
  KV_SERVER_CMD='./bin/simplekv --http.addr $KV_HTTP_ADDR --data.dir $KV_DATA_DIR' go test ./tests/e2e
  ```
  The harness will pick a free port, create a temp data dir, and wait for `/health` to succeed.
- Default (no env set): a stub server responds 501; tests are expected to fail until a real server exists.

## Mechanics
- Harness (`tests/e2e/harness.go`) manages server lifecycle, restart (for crash/recovery tests), and environment wiring.
- Minimal client (`tests/e2e/client.go`) mirrors the OpenAPI contract, handling base64 keys/values and standard errors.
- Test cases (`tests/e2e/e2e_test.go`) cover CRUD overwrite/last-write-wins, range scans, idempotent writes, delete visibility, malformed key 400s, and crash-restart (only when restart is supported via `KV_SERVER_CMD`).

## Regenerating the Go API client from docs/server_api.yaml
- Prereq: install `oapi-codegen` (pinned recommendation):
  ```sh
  go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.3.0
  ```
- Generate:
  ```sh
  ./scripts/gen-e2e-client.sh
  ```
  This produces/updates `tests/e2e/apiclient/generated.go` (package `apiclient`) from `docs/server_api.yaml`. A `go:generate` hook (`tests/e2e/generate.go`) points to the same script, so `go generate ./tests/e2e` will also refresh it.
