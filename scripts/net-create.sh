#!/usr/bin/env bash
# Crée un réseau virtuel libvirt pour un lab.
#   net-create.sh NOM -s 10.10.0.0/24 [options]
#     -s, --subnet CIDR     sous-réseau (obligatoire) ; l'hôte prend la 1re adresse (.1)
#     -M, --mode MODE       nat (défaut) : accès Internet via l'hôte
#                           isolated     : pas de sortie, VMs + hôte uniquement
#                           route        : routé sans NAT (pratique pour interconnecter des labs)
#         --no-dhcp         pas de DHCP (IPs fixes uniquement)
#     -D, --domain DOM      domaine DNS local (défaut : NOM.$LABS_PKI_DOMAIN) ; les VMs s'y résolvent
#                           par nom : web1.NOM.$LABS_PKI_DOMAIN
#
# Le DHCP distribue la 2e moitié de la plage (ex : .128-.254 en /24) ; la 1re moitié reste
# libre pour les IPs fixes (vm-create.sh -n NOM:IP/CIDR).
# shellcheck source=lib/common.sh
source "$(dirname "$0")/lib/common.sh"
need_cmd virsh

name=; subnet=; mode=nat; dhcp=1; domain=
while (($#)); do
  case $1 in
    -s|--subnet) subnet=$2; shift ;;
    -M|--mode) mode=$2; shift ;;
    --no-dhcp) dhcp=0 ;;
    -D|--domain) domain=$2; shift ;;
    -h|--help) sed -n '2,15p' "$0"; exit 0 ;;
    -*) die "option inconnue : $1" ;;
    *) name=$1 ;;
  esac; shift
done
[[ -n $name && -n $subnet ]] || die "usage : net-create.sh NOM -s CIDR [-M nat|isolated|route] [--no-dhcp]"
domain=${domain:-$name.$LABS_PKI_DOMAIN}
[[ $mode =~ ^(nat|isolated|route)$ ]] || die "mode invalide : $mode"
[[ $subnet =~ ^([0-9]+\.){3}[0-9]+/([0-9]+)$ ]] || die "CIDR invalide : $subnet"
net_exists "$name" && die "le réseau $name existe déjà"

prefix=${subnet#*/}
((prefix >= 8 && prefix <= 29)) || die "préfixe /$prefix non supporté (8 à 29)"
size=$(( 1 << (32 - prefix) ))
network=$(( $(ip2int "${subnet%/*}") & ~(size - 1) & 0xFFFFFFFF ))
host_ip=$(int2ip $((network + 1)))
mask=$(int2ip $(( (0xFFFFFFFF << (32 - prefix)) & 0xFFFFFFFF )))

# Chevauchement avec un réseau libvirt existant ?
while read -r n; do
  [[ -n $n ]] || continue
  other=$(net_host_ip "$n") || continue; [[ -n $other ]] || continue
  (( ($(ip2int "$other") & ~(size - 1) & 0xFFFFFFFF) == network )) && die "sous-réseau déjà utilisé par le réseau $n"
done < <(virsh net-list --all --name)

xml=$(mktemp); trap 'rm -f "$xml"' EXIT
{
  echo "<network>"
  echo "  <name>$name</name>"
  [[ $mode != isolated ]] && echo "  <forward mode='$mode'/>"
  echo "  <domain name='$domain' localOnly='yes'/>"
  echo "  <ip address='$host_ip' netmask='$mask'>"
  ((dhcp)) && echo "    <dhcp><range start='$(int2ip $((network + size / 2)))' end='$(int2ip $((network + size - 2)))'/></dhcp>"
  echo "  </ip>"
  echo "</network>"
} >"$xml"

virsh net-define "$xml" >/dev/null
virsh net-autostart "$name" >/dev/null
virsh net-start "$name" >/dev/null
ok "réseau $name ($mode) : $subnet, domaine $domain, hôte/passerelle $host_ip, bridge $(virsh net-info "$name" | awk '/Bridge/ {print $2}')"
