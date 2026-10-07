#!/bin/sh
# Fix /data ownership on first start (named volume or bind mount), then run
# the requested command as the unprivileged xgift user.
#
#   docker compose run --rm xgift setup   -> runs the xgift CLI
#   docker compose up -d                  -> runs xgift-web
set -e

DATA_DIR="${XGIFT_DATA_DIR:-/data}"

if [ "$(id -u)" = "0" ]; then
  mkdir -p "$DATA_DIR"
  chown -R xgift:xgift "$DATA_DIR" 2>/dev/null || true
  case "$1" in
    xgift-web|xgift|sh|bash|/usr/local/bin/*) exec gosu xgift "$@" ;;
    "") exec gosu xgift xgift-web ;;
    *) exec gosu xgift xgift "$@" ;;
  esac
fi

exec "$@"
