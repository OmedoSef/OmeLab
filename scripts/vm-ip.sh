#!/usr/bin/env bash
# Relais : IP d'une VM : VM [-w [SECONDES]]
# Porté en Go : utiliser « labctl vm ip ». Ce script garde l'ancienne syntaxe le temps de migrer les appels.
# shellcheck source=lib/relay.sh
source "$(dirname "$0")/lib/relay.sh"
# Ancienne syntaxe : « -w » seul attendait 180 s
if [[ ${2:-} == -w && -z ${3:-} ]]; then set -- "$1" -w 180; fi
relay vm ip "$@"
