#!/usr/bin/env bash
# Runs the live suite against the example's stack: brings it up with the
# generated e2e suite (`w17ctl test --keep`), runs this package inside the
# stack's network, and tears the stack down.
#
#   livetest/run.sh            # from the example's root, after `w17ctl codegen`
#   KEEP=1 livetest/run.sh     # leave the stack running afterwards
set -euo pipefail
cd "$(dirname "$0")/.."
project="reference-live-$$"
cleanup() {
  if [ -z "${KEEP:-}" ]; then docker compose -p "$project" down -v >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

w17ctl test --keep --project "$project"

# A gateway whose auth method binds tokens to device keys checks each device
# proof against its configured public origin (<DOMAIN>_GATEWAY_DPOP_ORIGIN),
# not against the address the suite reaches it at on the stack's network; the
# suite signs for that origin. Empty when the gateway declares none: then the
# proofs name the address dialled, and a gateway that binds nothing ignores
# them anyway.
gateway="$(docker compose -p "$project" ps -q app-gateway)"
[ -n "$gateway" ] || { echo "livetest: no app-gateway container" >&2; exit 1; }
dpop_origin="$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$gateway" \
  | sed -n 's/^[A-Z0-9_]*_GATEWAY_DPOP_ORIGIN=//p' | head -n 1)"

# In CI the job serves its Go module cache as a proxy (W17_GOPROXY_CONTAINER,
# adoption.yml); the suite's container is on the stack's network, so the
# proxy joins it and is dialled at its address there.
goproxy=()
if [ -n "${W17_GOPROXY_CONTAINER:-}" ]; then
  docker network connect "${project}_default" "$W17_GOPROXY_CONTAINER" >/dev/null 2>&1 || true
  # Off the stack's network before it is torn down, which a container still
  # attached would stop.
  trap 'docker network disconnect -f "${project}_default" "$W17_GOPROXY_CONTAINER" >/dev/null 2>&1 || true; cleanup' EXIT
  ip="$(docker inspect -f "{{(index .NetworkSettings.Networks \"${project}_default\").IPAddress}}" "$W17_GOPROXY_CONTAINER")"
  goproxy=(-e "GOPROXY=http://$ip,https://proxy.golang.org,direct" -e GODEBUG=http2client=0)
fi
# The suite serves the fake Sentry the stack reports its errors to
# (compose.yaml: W17_SENTRY_DSN names livetest-sentry).
docker run --rm --network "${project}_default" --network-alias livetest-sentry "${goproxy[@]}" \
  -e GATEWAY_DPOP_ORIGIN="$dpop_origin" \
  -u "$(id -u):$(id -g)" -e HOME=/tmp -e GOFLAGS=-buildvcs=false -e GOWORK=off \
  -v "$PWD:/w" -w /w/livetest golang:1.26 \
  go test -count=1 -race ./...
