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
# A failing suite shows what the services said: an assertion that waits on
# a background delivery (a Brevo contact, an error report) names only what
# it did not see, and the stack is gone once this script exits.
# The bus is in it because the marketing flake (a sign-up's UserCreated that
# never reached the plugin within 30 s, app-storage silent) can only be told
# apart there: an event never published, or published and never delivered.
logs() {
  echo "livetest: the suite failed — the last lines of app-storage, app-eventbus, nats and brevostub:" >&2
  docker compose -p "$project" logs --no-color --timestamps --tail 200 app-storage app-eventbus nats brevostub >&2 || true
}

# The suite serves the fake Sentry the stack reports its errors to
# (compose.yaml: W17_SENTRY_DSN names livetest-sentry).
docker run --rm --network "${project}_default" --network-alias livetest-sentry "${goproxy[@]}" \
  -e GATEWAY_DPOP_ORIGIN="$dpop_origin" \
  -u "$(id -u):$(id -g)" -e HOME=/tmp -e GOFLAGS=-buildvcs=false -e GOWORK=off \
  -v "$PWD:/w" -w /w/livetest golang:1.26 \
  go test -count=1 -race ./... || { logs; exit 1; }

# Second phase: the gateway's per-address limit on, as a deployment runs it.
# The test overlay switched it off for the suites above, which register many
# accounts from one address. A second gateway joins the stack — the same
# image, volumes and environment, minus the *_PUBLIC_RATELIMIT_* variables
# that switched it off, so each endpoint runs at what it declares — and
# TestPublicLimits_* runs against it. Beside the first rather than in its
# place: `w17ctl test` composed the stack with an override it has removed,
# and compose would not recreate the gateway onto the same network without it.
limited="${project}-gateway-limits"
limits_env="$(mktemp)"
trap 'rm -f "$limits_env"; docker rm -f "$limited" >/dev/null 2>&1 || true; docker network disconnect -f "${project}_default" "${W17_GOPROXY_CONTAINER:-none}" >/dev/null 2>&1 || true; cleanup' EXIT
docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$gateway" \
  | grep -v -e '_PUBLIC_RATELIMIT_' -e '^HOSTNAME=' -e '^$' >"$limits_env"
# How the first one runs, as `w17ctl test` started it (a test stack may run the
# bundle from source, `go run`, in another image than the bundle's own).
# One element a line; the last line is the one inspect ends with, not an element.
config_list() { docker inspect -f "{{range .Config.$1}}{{println .}}{{end}}" "$gateway" | sed '${/^$/d}'; }
mapfile -t entry < <(config_list Entrypoint)
mapfile -t cmd < <(config_list Cmd)
run_as=(-w "$(docker inspect -f '{{.Config.WorkingDir}}' "$gateway")")
user="$(docker inspect -f '{{.Config.User}}' "$gateway")"
[ -n "$user" ] && run_as+=(-u "$user")
if [ "${#entry[@]}" -gt 0 ]; then
  run_as+=(--entrypoint "${entry[0]}")
  cmd=("${entry[@]:1}" "${cmd[@]}")
fi
docker run -d --name "$limited" --network "${project}_default" --network-alias app-gateway-limits \
  --volumes-from "$gateway" --env-file "$limits_env" "${run_as[@]}" \
  "$(docker inspect -f '{{.Config.Image}}' "$gateway")" "${cmd[@]}" >/dev/null
docker run --rm --network "${project}_default" --network-alias livetest-sentry "${goproxy[@]}" \
  -e GATEWAY_URL=http://app-gateway-limits:8080 -e GATEWAY_DPOP_ORIGIN="$dpop_origin" -e PUBLIC_LIMITS_ON=1 \
  -u "$(id -u):$(id -g)" -e HOME=/tmp -e GOFLAGS=-buildvcs=false -e GOWORK=off \
  -v "$PWD:/w" -w /w/livetest golang:1.26 \
  go test -count=1 -race -run '^TestPublicLimits_' ./... || { docker logs --tail 100 "$limited" >&2; logs; exit 1; }
