#!/bin/sh
set -eu
# A newly attached volume belongs to root. Initialize its mount point once,
# then run the application without root privileges.
if [ "$(id -u)" = 0 ]; then
  chown sourcegraph:sourcegraph "${SOURCEGRAPH_DATA_ROOT:-/data}"
  exec runuser -u sourcegraph -- /usr/local/bin/repo-service
fi
exec /usr/local/bin/repo-service
