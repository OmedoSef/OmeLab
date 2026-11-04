#!/usr/bin/env bash
# Relais : images cloud — --list | [-f] IMAGE... (sans image : celle par défaut)
# Porté en Go : utiliser « labctl image list | get ». Ce script garde l'ancienne syntaxe le temps de migrer les appels.
# shellcheck source=lib/relay.sh
source "$(dirname "$0")/lib/relay.sh"
case ${1:-} in
  -l|--list) relay image list ;;
  -h|--help) relay image --help ;;
esac
args=("$@")
if [[ ${#args[@]} -eq 0 || ( ${#args[@]} -eq 1 && ( ${args[0]} == -f || ${args[0]} == --force ) ) ]]; then
  # shellcheck source=../scripts/config.env
  source "$(dirname "$0")/config.env"
  args+=("$LABS_DEFAULT_IMAGE")
fi
relay image get "${args[@]}"
