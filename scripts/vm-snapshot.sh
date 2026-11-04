#!/usr/bin/env bash
# Relais : snapshots : create VM NOM [description] | list VM | revert VM NOM | delete VM NOM
# Porté en Go : utiliser « labctl vm snapshot ». Ce script garde l'ancienne syntaxe le temps de migrer les appels.
# shellcheck source=lib/relay.sh
source "$(dirname "$0")/lib/relay.sh"
relay vm snapshot "$@"
