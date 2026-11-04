#!/usr/bin/env bash
# Affiche l'IP d'une VM.  vm-ip.sh VM [-w SECONDES]  (-w : attend qu'elle en ait une)
# shellcheck source=lib/common.sh
source "$(dirname "$0")/lib/common.sh"
vm=${1:?usage : vm-ip.sh VM [-w SECONDES]}
vm_exists "$vm" || die "VM inconnue : $vm"
if [[ ${2:-} == -w ]]; then wait_vm_ip "$vm" "${3:-180}" || die "pas d'IP pour $vm"
else vm_ip "$vm" || die "pas d'IP pour $vm (VM démarrée ? qemu-guest-agent installé ?)"; fi
