#!/usr/bin/env bash
# Relais : crée un réseau : NOM -s CIDR [-M nat|isolated|route] [--no-dhcp] [-D DOMAINE]
# Porté en Go : utiliser « labctl net create ». Ce script garde l'ancienne syntaxe le temps de migrer les appels.
# shellcheck source=lib/relay.sh
source "$(dirname "$0")/lib/relay.sh"
relay net create "$@"
