# shellcheck shell=bash
# Relais vers labctl pour les scripts portés en Go (même syntaxe que l'ancien script bash).
# Usage, dans un script : source "$(dirname "$0")/lib/relay.sh"; relay vm list "$@"
LABS="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
relay() {
  if [[ ! -x $LABS/bin/labctl ]]; then
    "$LABS/labctl/build.sh" build >&2 || "$LABS/labctl/build.sh" docker >&2
  fi
  exec "$LABS/bin/labctl" "$@"
}
