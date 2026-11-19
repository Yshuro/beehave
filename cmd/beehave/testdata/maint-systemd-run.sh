#!/bin/sh
# The stand-in for systemd-run, so that a step with max_new_processes runs on a host with no systemd user manager.
# Usage: systemd-run OPTIONS -- COMMAND ARGS. It starts the command and sets no limit. The limit itself is tested in internal/proc.
PATH=${PATH:-/usr/bin:/bin}
while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do shift; done
shift
exec "$@"
