#!/usr/bin/env bash
# Relais : liste les réseaux libvirt
# Porté en Go : utiliser « labctl net list ». Ce script garde l'ancienne syntaxe le temps de migrer les appels.
# shellcheck source=lib/relay.sh
source "$(dirname "$0")/lib/relay.sh"
relay net list "$@"
