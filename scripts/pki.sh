#!/usr/bin/env bash
# Relais : la PKI est désormais en Go dans labctl (labctl/internal/pki) ; utiliser « labctl pki ... ».
# Ce script reste le temps de migrer les appels existants (modules, habitudes) ; même syntaxe :
#   pki.sh init | sub-ca NOM -D DOMAINE [-s CIDR] | issue NOM [-C CA] [-D DOM] [-d a,b] [-i x,y] [--client] [--days N] [--pfx]
#   pki.sh list | show NOM | trust-vm VM... | deploy VM NOM [DOSSIER] | trust-host
set -euo pipefail
LABS="$(cd "$(dirname "$0")/.." && pwd)"
if [[ ! -x $LABS/bin/labctl ]]; then
  "$LABS/labctl/build.sh" build >&2 || "$LABS/labctl/build.sh" docker >&2
fi
exec "$LABS/bin/labctl" pki "$@"
