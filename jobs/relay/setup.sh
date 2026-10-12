#!/bin/sh
# One-shot, before anything else starts: every identity the pool pins.
# Idempotent (`relay mint` keeps an identity it finds), so a restarted stack
# keeps its pins.
#
# Keys stay in the volume of the one service that presents them. The
# fingerprints the others pin are public, and go to /pins, so the relay and
# `register` mount no other service's key.
set -eu
# The image's shell (busybox ash) has pipefail.
# shellcheck disable=SC3040
set -o pipefail

# fingerprint FILE CMD...: runs CMD and writes the fingerprint on the first
# line of its output to FILE. A failure leaves FILE as it was, so a stack
# that cannot mint is stopped here, not later by an empty pin.
fingerprint() {
  file=$1
  shift
  "$@" | awk 'NR == 1 { print $2 }' > "$file.tmp"
  test -s "$file.tmp"
  mv "$file.tmp" "$file"
}

# The control plane's client identity: the relay lets only its fingerprint
# manage it.
fingerprint /pins/control-plane.fingerprint \
  relay mint --dir /control-plane --name control-plane

# The relay's own identity, and the authority it issues worker certificates
# from.
fingerprint /pins/relay.fingerprint \
  relay mint --dir /relay --name relay --ca

# Each worker gets its certificate straight from the relay's authority, and
# the relay's fingerprint to pin. In a real pool a worker enrols with a
# one-time registration code instead (the Relays page of the admin issues
# them); issuing directly is the offline path for machines an operator
# prepares himself.
for w in worker-a worker-b; do
  relay issue-worker --identity-dir /relay --out "/$w" --name "$w"
  cp /pins/relay.fingerprint "/$w/relay.fingerprint"
done

# The files belong to whoever reads them: the relay (uid 10001), the storage
# bundle that presents the control plane's identity, and the workers. Each
# key is 0600 to its owner (`relay mint` and `issue-worker` write it so).
chown -R 10001 /relay
chown -R "${CONTROL_PLANE_UID:-65532}" /control-plane
chown -R "${WORKER_UID:-65532}" /worker-a /worker-b
chmod 0644 /pins/control-plane.fingerprint /pins/relay.fingerprint
