#!/bin/sh
set -eu

APP_UID="${PUID:-10001}"
APP_GID="${PGID:-10001}"

if ! getent group "$APP_GID" >/dev/null 2>&1; then
    groupadd --gid "$APP_GID" pics-runtime
fi

APP_GROUP="$(getent group "$APP_GID" | cut -d: -f1)"

if ! getent passwd "$APP_UID" >/dev/null 2>&1; then
    useradd --uid "$APP_UID" --gid "$APP_GID" \
        --home-dir /var/lib/pics-manager --no-create-home pics-runtime
fi

for dir in \
    /data/logs \
    /data/backups \
    /media/inbox \
    /media/staging \
    /media/library \
    /media/quarantine
do
    mkdir -p "$dir"
    chown "$APP_UID:$APP_GID" "$dir" 2>/dev/null || true
done

exec gosu "$APP_UID:$APP_GROUP" "$@"
