#!/usr/bin/env bash
# Runs the live suite against the example's stack, after two checks on the
# generated files that need no running stack:
#
#   1. the stack declares no standalone service for a component folded into
#      app-server (each would run twice, and a second eventbus subscriber
#      would deliver every event twice);
#   2. app-server's Dockerfile builds the admin console: the folded admin
#      serves it from the binary, and only a real `vite build` puts it there.
#
# Then it brings the stack up with the generated e2e suite
# (`w17ctl test --keep`), runs this package inside the stack's network, and
# tears the stack down.
#
#   livetest/run.sh            # from the example's root, after `w17ctl codegen`
#   KEEP=1 livetest/run.sh     # leave the stack running afterwards
set -euo pipefail
cd "$(dirname "$0")/.."
project="single-binary-live-$$"
spa_tag="$project-spa"
cleanup() {
  docker rmi "$spa_tag" >/dev/null 2>&1 || true
  if [ -z "${KEEP:-}" ]; then docker compose -p "$project" down -v >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

echo "==> the stack declares app-server and no folded component on its own"
services="$(docker compose -p "$project" config --services | sort)"
printf '%s\n' "$services" | sed 's/^/    /'
for component in app-storage app-business app-eventbus app-admin app-gateway; do
  if printf '%s\n' "$services" | grep -qx "$component"; then
    echo "$component runs as its own service beside app-server, which already serves it" >&2
    exit 1
  fi
done
printf '%s\n' "$services" | grep -qx app-server || { echo "no app-server service" >&2; exit 1; }

echo "==> app-server's Dockerfile builds the admin console"
docker build --target spa -f w17/services/app-server/Dockerfile -t "$spa_tag" .
docker run --rm "$spa_tag" sh -c '
  set -e
  dist=/workspace/w17/services/app-admin/spa/dist
  [ -f "$dist/index.html" ] || { echo "no index.html in $dist"; exit 1; }
  ref=$(grep -o "assets/[^\"]*\.js" "$dist/index.html" | head -1)
  [ -n "$ref" ] || { echo "index.html references no assets/*.js"; cat "$dist/index.html"; exit 1; }
  [ -f "$dist/$ref" ] || { echo "index.html references $ref, which dist does not contain"; ls -R "$dist"; exit 1; }
  size=$(wc -c < "$dist/$ref")
  [ "$size" -gt 10240 ] || { echo "$ref is only $size bytes: a placeholder, not a built console"; exit 1; }
  echo "    $ref ($size bytes)"
'

w17ctl test --keep --project "$project"

# The CLI of a composed domain: app-server runs the command tree itself, over
# the tiers it builds in-process for the one command — they listen on no
# network for a standalone CLI to dial. A write through the business facade,
# then two storage reads of what it wrote.
echo "==> app-server cli: a command runs in the composed binary, over its in-process tiers"
# The binary the container runs: the generated image's `/server`, or — where a
# dev overlay runs the bundle from source — `go run ./src`, the same binary.
cid="$(docker compose -p "$project" ps -q app-server)"
if docker inspect -f '{{json .Config.Cmd}}' "$cid" | grep -q 'go run'; then
  server=(go run ./src)
else
  server=(/server)
fi
cli() { docker compose -p "$project" exec -T app-server "${server[@]}" cli "$@"; }
owner=0ad10000-0000-7000-8000-0000000000e2
created="$(cli note create "written by app-server cli" --owner-id "$owner" --body "from the operator CLI")"
echo "    note create: $created"
id="$(printf '%s' "$created" | sed -n 's/.*"id":"\{0,1\}\([0-9][0-9]*\).*/\1/p')"
[ -n "$id" ] || { echo "note create printed no id" >&2; exit 1; }
# The facade tags the title ("[checked] …", so the write went through it),
# and the NoteChecked subscriber may already have stamped it again.
got="$(cli note get "$id")"
printf '%s' "$got" | grep -q "\"id\":\"$id\"" && printf '%s' "$got" | grep -q '\[checked\] written by app-server cli' \
  || { echo "note get $id did not read the note back: $got" >&2; exit 1; }
cli note list --owner-id "$owner" --limit 100 | grep -q "written by app-server cli" \
  || { echo "note list did not list the note" >&2; exit 1; }
echo "    note get $id and note list read it back"

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
docker run --rm --network "${project}_default" "${goproxy[@]}" \
  -u "$(id -u):$(id -g)" -e HOME=/tmp -e GOFLAGS=-buildvcs=false -e GOWORK=off \
  -v "$PWD:/w" -w /w/livetest golang:1.26 \
  go test -count=1 -race ./...
