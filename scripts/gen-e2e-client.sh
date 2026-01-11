#!/usr/bin/env sh
set -euo pipefail

# Generates an API client for tests from docs/server_api.yaml.

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
SPEC="$ROOT_DIR/docs/server_api.yaml"
OUT_DIR="$ROOT_DIR/tests/e2e/apiclient"
OUT_FILE="$OUT_DIR/generated.go"
PACKAGE="apiclient"
GENERATOR=${GENERATOR:-oapi-codegen}
GEN_VERSION_HINT="go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.3.0"

if ! command -v "$GENERATOR" >/dev/null 2>&1; then
  echo "error: $GENERATOR not found in PATH. Install with:"
  echo "  $GEN_VERSION_HINT"
  exit 1
fi

mkdir -p "$OUT_DIR"

echo "Generating client into $OUT_FILE from $SPEC..."
"$GENERATOR" \
  -generate types,client \
  -package "$PACKAGE" \
  -o "$OUT_FILE" \
  "$SPEC"

echo "Done. Generated client package: $PACKAGE (path: tests/e2e/apiclient)"
