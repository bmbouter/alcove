#!/bin/sh
set -e

PGDATA=/var/lib/postgresql/data

# Initialize PostgreSQL if not already done.
# Running initdb at startup (not build time) ensures the data directory
# is owned by the current UID, which is required by PostgreSQL and
# varies on OpenShift (random UID assignment).
if [ ! -f "$PGDATA/PG_VERSION" ]; then
  # PostgreSQL requires the data directory to be mode 0700.
  chmod 700 "$PGDATA" 2>/dev/null || true
  /usr/lib/postgresql/16/bin/initdb -D "$PGDATA"
  echo "host all all 0.0.0.0/0 trust" >> "$PGDATA/pg_hba.conf"
  echo "local all all trust" >> "$PGDATA/pg_hba.conf"
fi

# Start PostgreSQL
/usr/lib/postgresql/16/bin/pg_ctl -D "$PGDATA" \
  -o "-c listen_addresses=localhost -c dynamic_shared_memory_type=posix" \
  -l /tmp/postgresql.log start

# Wait for PostgreSQL to be ready
for i in $(seq 1 30); do
  /usr/lib/postgresql/16/bin/pg_isready -h /var/run/postgresql -q 2>/dev/null && break
  sleep 1
done

# Create the alcove database if it doesn't exist
/usr/lib/postgresql/16/bin/createdb -h /var/run/postgresql alcove 2>/dev/null || true

# Start NATS in the background
nats-server -p 4222 &

# Start the shim in the foreground (keeps the container alive)
exec /usr/local/bin/alcove-shim
