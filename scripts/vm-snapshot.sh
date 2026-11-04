#!/usr/bin/env bash
# Snapshots (internes qcow2, VM allumée ou éteinte).
#   vm-snapshot.sh create VM NOM [description]
#   vm-snapshot.sh list   VM
#   vm-snapshot.sh revert VM NOM
#   vm-snapshot.sh delete VM NOM
# shellcheck source=lib/common.sh
source "$(dirname "$0")/lib/common.sh"
need_cmd virsh
usage() { sed -n '2,6p' "$0"; exit 1; }

action=${1:-}; vm=${2:-}; snap=${3:-}
[[ -n $action && -n $vm ]] || usage
vm_exists "$vm" || die "VM inconnue : $vm"
case $action in
  create) [[ -n $snap ]] || usage
          virsh snapshot-create-as "$vm" "$snap" ${4:+--description "$4"} --atomic >/dev/null
          ok "snapshot $snap créé sur $vm" ;;
  list)   virsh snapshot-list "$vm" --tree 2>/dev/null || virsh snapshot-list "$vm" ;;
  revert) [[ -n $snap ]] || usage
          virsh snapshot-revert "$vm" "$snap"
          ok "$vm restaurée à $snap" ;;
  delete) [[ -n $snap ]] || usage
          virsh snapshot-delete "$vm" "$snap" >/dev/null
          ok "snapshot $snap supprimé de $vm" ;;
  *) usage ;;
esac
