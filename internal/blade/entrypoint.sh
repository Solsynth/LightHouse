#!/bin/sh
# One image ships both Blade binaries: the gateway is the default process, and
# `relay` selects the L4 relay node. Anything else is passed to the gateway.
set -e

case "${1:-}" in
  relay) shift; exec /app/relay "$@" ;;
  blade) shift; exec /app/blade "$@" ;;
  *) exec /app/blade "$@" ;;
esac
