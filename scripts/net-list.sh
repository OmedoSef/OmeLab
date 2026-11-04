#!/usr/bin/env bash
# Liste les réseaux libvirt avec mode, sous-réseau, bridge et VMs connectées.
# shellcheck source=lib/common.sh
source "$(dirname "$0")/lib/common.sh"
declare -A attached
while read -r vm; do
  [[ -n $vm ]] || continue
  for n in $(virsh domiflist "$vm" | awk '$2=="network" {print $3}'); do [[ " ${attached[$n]:-}" == *" $vm "* ]] || attached[$n]+="$vm "; done
done < <(virsh list --all --name)
printf '%-16s %-9s %-8s %-18s %-10s %s\n' NOM ETAT MODE "IP HOTE" BRIDGE VMS
while read -r n; do
  [[ -n $n ]] || continue
  xml=$(virsh net-dumpxml "$n")
  ip=$(grep -m1 -oP "<ip address='\K[^']+" <<<"$xml" || true)
  mask=$(grep -m1 -oP "<ip address='[^']+' netmask='\K[^']+" <<<"$xml" || true)
  bits=0; [[ -n $mask ]] && { m=$(ip2int "$mask"); while ((m & 0x80000000)); do bits=$((bits + 1)); m=$(((m << 1) & 0xFFFFFFFF)); done; }
  mode=$(grep -m1 -oP "<forward mode='\K[^']+" <<<"$xml" || true); mode=${mode:-isolated}
  state=$(virsh net-info "$n" | awk '/^Active/ {print ($2=="yes" ? "actif" : "inactif")}')
  printf '%-16s %-9s %-8s %-18s %-10s %s\n' "$n" "$state" "$mode" "${ip:--}${mask:+/$bits}" \
    "$(virsh net-info "$n" | awk '/Bridge/ {print $2}')" "${attached[$n]:--}"
done < <(virsh net-list --all --name </dev/null | sort)
