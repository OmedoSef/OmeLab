#!/usr/bin/env bash
# Relais : liste les VMs (état, lab, IP) — options -l LAB, -m
# Porté en Go : utiliser « labctl vm list ». Ce script garde l'ancienne syntaxe le temps de migrer les appels.
# shellcheck source=lib/relay.sh
source "$(dirname "$0")/lib/relay.sh"
relay vm list "$@"
