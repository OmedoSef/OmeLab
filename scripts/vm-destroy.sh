#!/usr/bin/env bash
# Supprime des VMs (arrêt, suppression de la définition et de leur dossier dans $LABS_VM_DIR).
#   vm-destroy.sh [-y] [--force] VM...
#     -y        pas de confirmation
#     --force   autorise la suppression d'une VM qui n'a pas été créée par ces scripts
#               (sa définition est supprimée, ses disques hors $LABS_VM_DIR sont conservés)
# shellcheck source=lib/common.sh
source "$(dirname "$0")/lib/common.sh"
need_cmd virsh

force=0; vms=()
while (($#)); do
  case $1 in
    -y|--yes) export ASSUME_YES=1 ;;
    --force) force=1 ;;
    -h|--help) sed -n '2,6p' "$0"; exit 0 ;;
    *) vms+=("$1") ;;
  esac; shift
done
((${#vms[@]})) || die "usage : vm-destroy.sh [-y] [--force] VM..."

for vm in "${vms[@]}"; do
  vm_exists "$vm" || { warn "$vm n'existe pas"; continue; }
  if ! vm_is_managed "$vm" && ((force == 0)); then
    warn "$vm n'a pas été créée par ces scripts, ignorée (utiliser --force)"; continue
  fi
  confirm "Supprimer définitivement la VM $vm et ses disques ?" || continue
  mapfile -t files < <(virsh domblklist "$vm" --details | awk '$1=="file" && $4!="-" {print $4}')
  [[ $(vm_state "$vm") == "shut off" ]] || virsh destroy "$vm" >/dev/null
  virsh undefine "$vm" --nvram --snapshots-metadata --managed-save >/dev/null 2>&1 \
    || virsh undefine "$vm" --snapshots-metadata >/dev/null
  for f in "${files[@]}"; do
    [[ $f == "$LABS_VM_DIR/"* ]] || warn "disque conservé (hors $LABS_VM_DIR) : $f"
  done
  [[ -d "$LABS_VM_DIR/$vm" ]] && rm -rf -- "${LABS_VM_DIR:?}/$vm"
  # virt-install crée un pool libvirt pour le dossier de la VM : on le retire aussi
  while read -r pool; do
    [[ -n $pool && $(virsh pool-dumpxml "$pool" 2>/dev/null) == *"<path>$LABS_VM_DIR/$vm</path>"* ]] || continue
    virsh pool-destroy "$pool" >/dev/null 2>&1 || true
    virsh pool-undefine "$pool" >/dev/null
  done < <(virsh pool-list --all --name)
  ok "$vm supprimée"
done
