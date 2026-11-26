#!/bin/sh
# Runs BrokerBuddy as PUID:PGID (default 1000:1000), like linuxserver.io images,
# so files in /data belong to the matching user on the host.
set -e

if [ "$(id -u)" = "0" ]; then
    PUID="${PUID:-1000}"
    PGID="${PGID:-1000}"
    mkdir -p /data
    find /data -xdev \( ! -user "$PUID" -o ! -group "$PGID" \) -exec chown -h "$PUID:$PGID" {} +
    exec su-exec "$PUID:$PGID" brokerbuddy -db /data/brokerbuddy.db -addr 0.0.0.0:8443 "$@"
fi

# Started with --user: run as that user and leave ownership alone.
exec brokerbuddy -db /data/brokerbuddy.db -addr 0.0.0.0:8443 "$@"
