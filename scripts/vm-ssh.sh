#!/usr/bin/env bash
# Relais : SSH vers une VM par son nom — [-u USER] VM [commande...]
# Porté en Go : utiliser « labctl vm ssh ». Ce script garde l'ancienne syntaxe le temps de migrer les appels.
# shellcheck source=lib/relay.sh
source "$(dirname "$0")/lib/relay.sh"
relay vm ssh "$@"
