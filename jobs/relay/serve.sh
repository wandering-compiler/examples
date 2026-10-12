#!/bin/sh
# Starts the relay pinned to the control plane's identity.
set -eu
RELAY_CONTROL_PLANE_FINGERPRINT="$(cat /pins/control-plane.fingerprint)"
export RELAY_CONTROL_PLANE_FINGERPRINT
exec relay "$@"
