#!/bin/sh
set -e

# Start PostgreSQL in the background
/usr/lib/postgresql/16/bin/pg_ctl -D /var/lib/postgresql/data \
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
