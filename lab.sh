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
  printf '  %-10s %s\n' core "passerelle core-gw (intégrée à labctl) : Internet, DHCP, DNS, liens entre modules"
  for d in "$MODULES_DIR"/*/; do
    m=$(basename "$d")
    if [[ -f $d/module.sh ]]; then desc=$(sed -n '2s/^# *//p' "$d/module.sh")
    elif [[ -f $d/module.yaml ]]; then desc=$(sed -n 's/^description: *//p' "$d/module.yaml")
    else continue; fi
    printf '  %-10s %s\n' "$m" "$desc"
  done
  exit 0
fi
module=$1; shift
labctl="$(dirname "$MODULES_DIR")/bin/labctl"
# La passerelle (core) est intégrée à labctl
if [[ $module == core ]]; then
  [[ -x $labctl ]] || { echo "labctl absent : ./labctl/build.sh" >&2; exit 1; }
  [[ $# -gt 0 ]] || set -- --help
  exec "$labctl" core "$@"
fi
if [[ -x $MODULES_DIR/$module/module.sh ]]; then
  exec "$MODULES_DIR/$module/module.sh" "$@"
fi
# Module déclaratif (module.yaml seul) : moteur de labctl
if [[ -f $MODULES_DIR/$module/module.yaml ]]; then
  [[ -x $labctl ]] || { echo "labctl absent : ./labctl/build.sh" >&2; exit 1; }
  [[ $# -gt 0 ]] || set -- --help
  exec "$labctl" module "$module" "$@"
fi
echo "module inconnu : $module (./lab.sh pour la liste)" >&2; exit 1
