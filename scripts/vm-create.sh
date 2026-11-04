#!/usr/bin/env bash
# Relais : crée une VM depuis une image cloud — NOM [-i IMAGE] [-c CPUS] [-m MIB] [-d GIB] [-n RESEAU[:IP/CIDR]]...
#   [--gw IP] [--dns IP,IP] [-l LAB] [-p a,b] [-f SRC:DEST[:MODE]]... [-r CMD]... [--user-data F] [--no-ca] [-w] [--dry-run]
# Porté en Go : utiliser « labctl vm create ». Ce script garde l'ancienne syntaxe le temps de migrer les appels.
# shellcheck source=lib/relay.sh
source "$(dirname "$0")/lib/relay.sh"
relay vm create "$@"
