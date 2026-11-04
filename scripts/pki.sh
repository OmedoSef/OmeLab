#!/usr/bin/env bash
# Relais : PKI des labs (init, sub-ca, issue, list, show, trust-vm, deploy, trust-host, renew, archives)
# Porté en Go : utiliser « labctl pki ». Ce script garde l'ancienne syntaxe le temps de migrer les appels.
# shellcheck source=lib/relay.sh
source "$(dirname "$0")/lib/relay.sh"
relay pki "$@"
