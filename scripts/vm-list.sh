#!/usr/bin/env bash
# Liste les VMs avec leur état, leur lab et leur IP.
#   vm-list.sh               toutes les VMs
#   vm-list.sh -l LAB        seulement celles d'un lab
#   vm-list.sh -m            seulement celles créées par ces scripts
# shellcheck source=lib/common.sh
source "$(dirname "$0")/lib/common.sh"
need_cmd virsh

lab=; managed=0
while (($#)); do
  case $1 in
    -l|--lab) lab=$2; shift ;;
    -m|--managed) managed=1 ;;
    -h|--help) sed -n '2,5p' "$0"; exit 0 ;;
    *) die "option inconnue : $1" ;;
  esac; shift
done

printf '%-28s %-10s %-12s %s\n' NOM ETAT LAB IP
while read -r vm; do
  [[ -n $vm ]] || continue
  vlab=$(vm_lab "$vm")
  [[ -z $lab || $vlab == "$lab" ]] || continue
  ((managed == 0)) || vm_is_managed "$vm" || continue
  state=$(vm_state "$vm"); ip=-
  [[ $state == running ]] && ip=$(vm_ip "$vm" || echo "?")
  printf '%-28s %-10s %-12s %s\n' "$vm" "$state" "${vlab:--}" "$ip"
done < <(virsh list --all --name </dev/null | sort)
