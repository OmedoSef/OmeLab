#!/usr/bin/env bash
# Relais : supprime des VMs — [-y] [--force] VM... (ASSUME_YES=1 équivaut à -y)
# Porté en Go : utiliser « labctl vm destroy ». Ce script garde l'ancienne syntaxe le temps de migrer les appels.
# shellcheck source=lib/relay.sh
source "$(dirname "$0")/lib/relay.sh"
relay vm destroy "$@"
