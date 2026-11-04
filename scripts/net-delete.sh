#!/usr/bin/env bash
# Relais : supprime un réseau : [-y] NOM
# Porté en Go : utiliser « labctl net delete ». Ce script garde l'ancienne syntaxe le temps de migrer les appels.
# shellcheck source=lib/relay.sh
source "$(dirname "$0")/lib/relay.sh"
relay net delete "$@"
