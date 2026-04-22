#!/bin/sh
# Fix /run ownership for s6-overlay on OpenShift.
# OpenShift runs containers with a random UID but GID 0 (root group).
# s6-overlay's preinit requires /run to be owned by the current UID.
# Since we run as GID 0, we can chmod /run (group write).
chmod 1777 /run 2>/dev/null || true
exec /init "$@"
