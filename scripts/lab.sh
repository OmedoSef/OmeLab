#!/usr/bin/env bash
# Actions groupées sur toutes les VMs d'un lab (VMs créées avec vm-create.sh -l LAB).
#   lab.sh list                     liste les labs existants
#   lab.sh status   LAB             VMs du lab, état et IP
#   lab.sh up       LAB             démarre toutes les VMs
#   lab.sh down     LAB             arrêt propre (ACPI), forcé après 120 s
#   lab.sh snapshot LAB NOM         snapshot de chaque VM
#   lab.sh revert   LAB NOM         restaure chaque VM au snapshot NOM
#   lab.sh destroy  LAB [-y]        supprime toutes les VMs du lab
# shellcheck source=lib/common.sh
source "$(dirname "$0")/lib/common.sh"
need_cmd virsh
usage() { sed -n '2,9p' "$0"; exit 1; }

action=${1:-}; lab=${2:-}
if [[ $action == list ]]; then
  while read -r vm; do [[ -n $vm ]] && vm_lab "$vm"; done < <(virsh list --all --name) | sort | uniq -c \
    | awk 'BEGIN {printf "%-20s %s\n", "LAB", "VMS"} {printf "%-20s %s\n", $2, $1}'
  exit 0
fi
[[ -n $action && -n $lab ]] || usage
mapfile -t vms < <(lab_vms "$lab")
((${#vms[@]})) || die "aucune VM dans le lab $lab"

case $action in
  status) exec "$SCRIPTS_DIR/vm-list.sh" -l "$lab" ;;
  up)
    for vm in "${vms[@]}"; do
      if [[ $(vm_state "$vm") == running ]]; then ok "$vm déjà démarrée"; else virsh start "$vm" >/dev/null; ok "$vm démarrée"; fi
    done ;;
  down)
    for vm in "${vms[@]}"; do [[ $(vm_state "$vm") == running ]] && virsh shutdown "$vm" >/dev/null; done
    for vm in "${vms[@]}"; do
      t=0; while [[ $(vm_state "$vm") != "shut off" ]] && ((t < 120)); do sleep 2; t=$((t + 2)); done
      [[ $(vm_state "$vm") == "shut off" ]] || { warn "$vm ne s'arrête pas, arrêt forcé"; virsh destroy "$vm" >/dev/null; }
      ok "$vm arrêtée"
    done ;;
  snapshot) [[ -n ${3:-} ]] || usage; for vm in "${vms[@]}"; do "$SCRIPTS_DIR/vm-snapshot.sh" create "$vm" "$3"; done ;;
  revert)   [[ -n ${3:-} ]] || usage; for vm in "${vms[@]}"; do "$SCRIPTS_DIR/vm-snapshot.sh" revert "$vm" "$3"; done ;;
  destroy)
    [[ ${3:-} == -y ]] && export ASSUME_YES=1
    confirm "Supprimer les ${#vms[@]} VMs du lab $lab (${vms[*]}) ?" || exit 0
    ASSUME_YES=1 "$SCRIPTS_DIR/vm-destroy.sh" "${vms[@]}" ;;
  *) usage ;;
esac
