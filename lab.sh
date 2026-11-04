#!/usr/bin/env bash
# Point d'entrée des labs : ./lab.sh <module> <commande> [arguments]
#
#   ./lab.sh                     liste les modules
#   ./lab.sh <module>            aide du module
#   ./lab.sh <module> <cmd> ...  exécute une commande du module (modules/<module>/module.sh)
set -euo pipefail
MODULES_DIR="$(cd "$(dirname "$0")/modules" && pwd)"

if [[ $# -eq 0 || $1 == -h || $1 == --help ]]; then
  echo "Usage : ./lab.sh <module> <commande> [arguments]"
  echo
  echo "Modules :"
  for m in "$MODULES_DIR"/*/module.sh; do
    printf '  %-10s %s\n' "$(basename "$(dirname "$m")")" "$(sed -n '2s/^# *//p' "$m")"
  done
  exit 0
fi
module=$1; shift
[[ -x $MODULES_DIR/$module/module.sh ]] || { echo "module inconnu : $module (./lab.sh pour la liste)" >&2; exit 1; }
exec "$MODULES_DIR/$module/module.sh" "$@"
