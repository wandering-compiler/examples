#!/usr/bin/env bash
# Runs the live suite against the example's stack: brings it up with the
# generated e2e suite (`w17ctl test --keep`), runs this package inside the
# stack's network, and tears the stack down.
#
#   livetest/run.sh            # from the example's root, after `w17ctl codegen`
#   KEEP=1 livetest/run.sh     # leave the stack running afterwards
set -euo pipefail
cd "$(dirname "$0")/.."
project="jobs-live-$$"
cleanup() {
  if [ -z "${KEEP:-}" ]; then docker compose -p "$project" down -v >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

w17ctl test --keep --project "$project"

docker run --rm --network "${project}_default" \
  -u "$(id -u):$(id -g)" -e HOME=/tmp -e GOFLAGS=-buildvcs=false -e GOWORK=off \
  -v "$PWD:/w" -w /w/livetest golang:1.26 \
  go test -count=1 -race ./...
