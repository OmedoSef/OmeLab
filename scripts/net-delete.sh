#!/usr/bin/env bash
# Supprime un réseau libvirt (refuse s'il est encore utilisé par une VM).
#   net-delete.sh [-y] NOM
# shellcheck source=lib/common.sh
source "$(dirname "$0")/lib/common.sh"
[[ ${1:-} == -y ]] && { export ASSUME_YES=1; shift; }
name=${1:?usage : net-delete.sh [-y] NOM}
[[ $name != default ]] || die "le réseau default n'est pas supprimé par ce script"
net_exists "$name" || die "réseau inconnu : $name"
users=()
while read -r vm; do
  [[ -n $vm ]] && virsh domiflist "$vm" | awk -v n="$name" '$2=="network" && $3==n {f=1} END {exit !f}' && users+=("$vm")
done < <(virsh list --all --name)
((${#users[@]} == 0)) || die "réseau utilisé par : ${users[*]}"
confirm "Supprimer le réseau $name ?" || exit 0
virsh net-destroy "$name" >/dev/null 2>&1 || true
virsh net-undefine "$name" >/dev/null
ok "réseau $name supprimé"
