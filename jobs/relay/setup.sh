#!/bin/sh
# One-shot, before anything else starts: every identity the pool pins.
# Idempotent (`relay mint` keeps an identity it finds), so a restarted stack
# keeps its pins.
set -eu

# The control plane's client identity: the relay lets only its fingerprint
# manage it.
relay mint --dir /control-plane --name control-plane | cut -d' ' -f2 > /control-plane/control-plane.fingerprint

# The relay's own identity, and the authority it issues worker certificates
# from.
relay mint --dir /relay --name relay --ca | head -1 | cut -d' ' -f2 > /relay/relay.fingerprint

# Each worker gets its certificate straight from the relay's authority, and
# the relay's fingerprint to pin. In a real pool a worker enrols with a
# one-time registration code instead (the Relays page of the admin issues
# them); issuing directly is the offline path for machines an operator
# prepares himself.
for w in worker-a worker-b; do
  relay issue-worker --identity-dir /relay --out "/$w" --name "$w"
  cp /relay/relay.fingerprint "/$w/relay.fingerprint"
done

# The files belong to whoever reads them: the relay (uid 10001), the storage
# bundle that holds the control plane's key, and the workers.
chown -R 10001 /relay
chown -R "${CONTROL_PLANE_UID:-65532}" /control-plane
chown -R "${WORKER_UID:-65532}" /worker-a /worker-b
chmod 0644 /control-plane/control-plane.fingerprint
