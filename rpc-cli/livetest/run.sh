#!/usr/bin/env bash
# Brings the stack up and checks the two surfaces this example declares, the
# public gRPC surface and the two CLI binaries, against the seeded rows of
# fixtures/ops/test.json:
#
#   1. the surface lists exactly the three public services, and not
#      CountJobs, which no pick names;
#   2. CreateJob is served under JobAdmin and not under JobMutation, where
#      its sibling CancelJob stays;
#   3. JobRead also answers to its second name, acme.ops.v1.JobRead, and no
#      undeclared name is served;
#   4. GetJob through the public surface returns a seeded row;
#   5. ops-gateway-cli, which dials the backend, runs `job count`;
#   6. ops-gateway-ctl, which has only the public surface, runs `job get` and
#      has no `job count`;
#   7. both binaries report the declared version and description.
#
# The generated e2e suite drives REST, MCP and the admin, and this domain has
# none of them, so the lock has `e2e: false` and this script is the test.
#
#   livetest/run.sh            # from the example's root, after `w17ctl codegen`
#   KEEP=1 livetest/run.sh     # leave the stack running afterwards
set -euo pipefail
cd "$(dirname "$0")/.."

# One name for every run, so the image builder `w17ctl stack up --build`
# makes for a compose project is made once and reused.
export COMPOSE_PROJECT_NAME=rpc-cli-live
grpcurl_image="fullstorydev/grpcurl:v1.9.3"
seed_id=710001
seed_name="seed reindex"

# The CLI binaries build from the source tree with the go.work beside this
# script, whose module paths may reach above the example (an example
# checked out inside a larger tree); the build container mounts the tree the
# example sits in.
top="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
rel="$(realpath --relative-to="$top" "$PWD")"
bin="$PWD/bin/livetest"

cleanup() {
  if [ -z "${KEEP:-}" ]; then docker compose down -v >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT

fail() { echo "FAILED: $*" >&2; exit 1; }

# A bind mount of a directory that does not exist is created by the Docker
# daemon, owned by root; the storage service mounts the dev certificates
# from here.
mkdir -p w17/certs/dev.local

# The database first, and its schema from the console: the seed step writes
# rows, so the tables have to exist before the rest of the stack starts.
echo "==> the database and its schema"
w17ctl stack up --build ops-postgres
echo "==> the stack"
w17ctl stack up --build --no-sync

gateway="$(docker compose ps -q ops-gateway)"
[ -n "$gateway" ] || fail "no ops-gateway container"
network="$(docker inspect -f '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{end}}' "$gateway")"
grpcurl() { docker run --rm --network "$network" "$grpcurl_image" -plaintext "$@"; }
surface="ops-gateway:8082"

echo "==> waiting for the public surface on $surface"
deadline=$((SECONDS + 180))
until services="$(grpcurl "$surface" list 2>&1)"; do
  [ "$SECONDS" -lt "$deadline" ] || { docker compose logs --tail=40 ops-gateway ops-storage; fail "the public surface did not answer: $services"; }
  sleep 2
done
printf '%s\n' "$services" | sed 's/^/    /'

echo "==> the public services, and no CountJobs"
for want in w17lock.ops.rpc.JobRead w17lock.ops.rpc.JobAdmin w17lock.ops.rpc.JobMutation acme.ops.v1.JobRead; do
  grep -qx "$want" <<<"$services" || fail "$want is not served"
done
grep -qx acme.ops.v1.JobAdmin <<<"$services" && fail "acme.ops.v1.JobAdmin is served, and nothing declares it"
for svc in w17lock.ops.rpc.JobRead w17lock.ops.rpc.JobAdmin w17lock.ops.rpc.JobMutation; do
  if grpcurl "$surface" list "$svc" | grep -q CountJobs; then
    fail "$svc serves CountJobs, which no pick names"
  fi
done

echo "==> CreateJob under JobAdmin, CancelJob under JobMutation"
admin="$(grpcurl "$surface" list w17lock.ops.rpc.JobAdmin)"
mutation="$(grpcurl "$surface" list w17lock.ops.rpc.JobMutation)"
grep -q 'JobAdmin.CreateJob' <<<"$admin" || fail "JobAdmin does not serve CreateJob: $admin"
grep -q 'JobMutation.CancelJob' <<<"$mutation" || fail "JobMutation does not serve CancelJob: $mutation"
grep -q CreateJob <<<"$mutation" && fail "JobMutation serves CreateJob as well as JobAdmin"

echo "==> JobRead/GetJob through the public surface"
got="$(grpcurl -d "{\"job_id\": $seed_id}" "$surface" w17lock.ops.rpc.JobRead/GetJob)" \
  || fail "GetJob failed: $got"
grep -q "$seed_name" <<<"$got" || fail "GetJob did not return the seeded row: $got"

echo "==> building ops-gateway-cli and ops-gateway-ctl"
mkdir -p "$bin"
docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -e GOFLAGS=-buildvcs=false -e CGO_ENABLED=0 \
  -v "$top:/w" -w "/w/$rel" golang:1.26 sh -c '
    set -e
    (cd w17/services/ops-gateway && go build -o /w/'"$rel"'/bin/livetest/ops-gateway-cli ./cmd/ops-gateway-cli)
    (cd w17/services/ops-ctl && go build -o /w/'"$rel"'/bin/livetest/ops-gateway-ctl ./cmd/ops-gateway-ctl)
  '
run() { docker run --rm --network "$network" -v "$bin:/bin-livetest:ro" "$@"; }

echo "==> ops-gateway-cli job count (through the backend)"
out="$(run -e OPS_GATEWAY_BACKEND_ADDR=ops-storage:50051 alpine /bin-livetest/ops-gateway-cli job count 2>&1)" \
  || fail "ops-gateway-cli job count failed: $out"
printf '%s\n' "$out" | sed 's/^/    /'
grep -Eq '"total": *"?3"?' <<<"$out" || fail "job count did not report the 3 seeded jobs"

echo "==> ops-gateway-ctl job get $seed_id (through the public surface)"
out="$(run -e OPS_GATEWAY_GATEWAY_ADDR="$surface" alpine /bin-livetest/ops-gateway-ctl job get "$seed_id" 2>&1)" \
  || fail "ops-gateway-ctl job get failed: $out"
grep -q "$seed_name" <<<"$out" || fail "ops-gateway-ctl job get did not return the seeded row: $out"

echo "==> ops-gateway-ctl has no job count"
if out="$(run -e OPS_GATEWAY_GATEWAY_ADDR="$surface" alpine /bin-livetest/ops-gateway-ctl job count 2>&1)"; then
  fail "ops-gateway-ctl ran job count, which has no public method to call: $out"
fi
echo "    refused: $(head -1 <<<"$out")"

echo "==> the declared version and description"
for b in ops-gateway-cli ops-gateway-ctl; do
  out="$(run alpine sh -c "/bin-livetest/$b --version; /bin-livetest/$b --help" 2>&1 || true)"
  grep -q v1 <<<"$out" || fail "$b --version does not report v1: $out"
  grep -q "Operator CLI for the ops domain" <<<"$out" || fail "$b --help does not print the description: $out"
done

echo "==> ok"
