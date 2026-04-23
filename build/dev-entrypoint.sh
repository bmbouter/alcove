#!/bin/sh
# Dev container entrypoint: starts PostgreSQL, NATS, and the shim.
#
# NOTE: No "set -e" — we want the shim to start even if PostgreSQL or NATS
# fail. The CI health check only needs the shim's /healthz endpoint; PG and
# NATS are tested separately. Using set -e caused the container to exit
# silently on Docker (GHA) whenever a non-critical command failed, and with
# --rm the logs were lost.

echo "[entrypoint] starting dev container (pid $$, uid $(id -u))"

# Use /tmp for all PostgreSQL state — it's always writable regardless of UID.
# /var/lib/postgresql and /var/run/postgresql are on the overlay filesystem
# and not reliably writable by OpenShift's random UIDs.
PGDATA=/tmp/pgdata
PGRUN=/tmp/pgrun
mkdir -p "$PGRUN"

# Initialize PostgreSQL if not already done.
# Running initdb at startup (not build time) ensures the data directory
# is owned by the current UID, which is required by PostgreSQL and
# varies on OpenShift (random UID assignment).
if [ ! -f "$PGDATA/PG_VERSION" ]; then
  echo "[entrypoint] initializing PostgreSQL data directory"
  mkdir -p "$PGDATA"
  if /usr/lib/postgresql/16/bin/initdb -D "$PGDATA" > /tmp/initdb.log 2>&1; then
    echo "[entrypoint] initdb succeeded"
    echo "host all all 0.0.0.0/0 trust" >> "$PGDATA/pg_hba.conf"
    echo "local all all trust" >> "$PGDATA/pg_hba.conf"
    # Use /tmp/pgrun for the Unix socket
    sed -i "s|#unix_socket_directories.*|unix_socket_directories = '$PGRUN'|" "$PGDATA/postgresql.conf"
  else
    echo "[entrypoint] WARNING: initdb failed (exit $?), PostgreSQL will not be available"
    cat /tmp/initdb.log 2>/dev/null || true
  fi
fi

# Start PostgreSQL (if initdb succeeded)
if [ -f "$PGDATA/PG_VERSION" ]; then
  echo "[entrypoint] starting PostgreSQL"
  if /usr/lib/postgresql/16/bin/pg_ctl -D "$PGDATA" \
    -o "-c listen_addresses=localhost -c dynamic_shared_memory_type=posix" \
    -l /tmp/postgresql.log start; then
    echo "[entrypoint] PostgreSQL started"

    # Wait for PostgreSQL to be ready
    for i in $(seq 1 30); do
      /usr/lib/postgresql/16/bin/pg_isready -h "$PGRUN" -q 2>/dev/null && break
      sleep 1
    done

    # Create the alcove database if it doesn't exist
    /usr/lib/postgresql/16/bin/createdb -h "$PGRUN" alcove 2>/dev/null || true
  else
    echo "[entrypoint] WARNING: pg_ctl start failed (exit $?), PostgreSQL will not be available"
    cat /tmp/postgresql.log 2>/dev/null || true
  fi
else
  echo "[entrypoint] skipping PostgreSQL start (no data directory)"
fi

# Start NATS in the background (with monitoring on 8222 for health checks)
echo "[entrypoint] starting NATS"
nats-server -p 4222 -m 8222 &

# Start the shim in the foreground (keeps the container alive)
echo "[entrypoint] starting shim"
exec /usr/local/bin/alcove-shim
