# shellcheck shell=bash disable=SC2034  # variables utilisées par les scripts qui sourcent ce fichier
# Fonctions partagées. À sourcer depuis les scripts : source "$(dirname "$0")/lib/common.sh"
set -euo pipefail

SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# config.local.env d'abord : config.env ne remplit que les variables encore vides.
# shellcheck source=/dev/null
[[ -f "$SCRIPTS_DIR/config.local.env" ]] && source "$SCRIPTS_DIR/config.local.env"
# shellcheck source=../config.env
source "$SCRIPTS_DIR/config.env"
export LIBVIRT_DEFAULT_URI
# Sortie de virsh non traduite : les scripts la parsent.
export LC_ALL=C.UTF-8
# virsh n'a jamais besoin de stdin ; sans ça il « mange » l'entrée des boucles while read.
virsh() { command virsh "$@" </dev/null; }

LABS_MARKER="managed-by=labs-scripts"

if [[ -t 2 ]]; then C_R=$'\e[31m' C_G=$'\e[32m' C_Y=$'\e[33m' C_B=$'\e[34m' C_0=$'\e[0m'; else C_R='' C_G='' C_Y='' C_B='' C_0=''; fi
info() { echo "${C_B}==>${C_0} $*" >&2; }
ok()   { echo "${C_G}ok${C_0}  $*" >&2; }
warn() { echo "${C_Y}attention:${C_0} $*" >&2; }
die()  { echo "${C_R}erreur:${C_0} $*" >&2; exit 1; }

need_cmd() { local c; for c; do command -v "$c" >/dev/null || die "commande manquante : $c"; done; }

confirm() {
  [[ "${ASSUME_YES:-0}" == 1 ]] && return 0
  local r; read -rp "$1 [o/N] " r; [[ "$r" =~ ^[oOyY]$ ]]
}

vm_exists()  { virsh dominfo "$1" &>/dev/null; }
net_exists() { virsh net-info "$1" &>/dev/null; }
net_active() { [[ $(virsh net-info "$1" 2>/dev/null | awk '/^Active:/ {print $2}') == yes ]]; }
vm_state()   { virsh domstate "$1" 2>/dev/null | head -1; }

# Description libvirt de la VM, ex : "managed-by=labs-scripts lab=k8s"
vm_desc() { virsh desc "$1" 2>/dev/null | grep -v '^No description' || true; }
vm_is_managed() { [[ "$(vm_desc "$1")" == *"$LABS_MARKER"* ]]; }
vm_lab() { vm_desc "$1" | grep -oP '(?<=lab=)\S+' || true; }

# VMs appartenant à un lab donné
lab_vms() {
  local lab=$1 vm
  while read -r vm; do
    [[ -n "$vm" && "$(vm_lab "$vm")" == "$lab" ]] && echo "$vm"
  done < <(virsh list --all --name)
}

# Première IPv4 (hors loopback) : bail DHCP libvirt, agent invité, table ARP, puis baux de la passerelle core.
vm_ip() {
  local vm=$1 src ip
  for src in lease agent arp; do
    ip=$(virsh domifaddr "$vm" --source "$src" 2>/dev/null \
      | awk '!f && $1!="lo" && $3=="ipv4" {split($4,a,"/"); if (a[1] !~ /^127\./) {print a[1]; f=1}}')
    [[ -n "$ip" ]] && { echo "$ip"; return 0; }
  done
  # Réseaux des modules : le DHCP est sur la passerelle core-gw, on lit ses baux
  if [[ $vm != core-gw && $(vm_state core-gw 2>/dev/null) == running ]]; then
    local macs gw
    macs=$(virsh domiflist "$vm" 2>/dev/null | awk 'NR>2 && NF {print tolower($5)}' | paste -sd'|')
    gw=$(vm_ip core-gw) || return 1
    ip=$(ssh "${SSH_OPTS[@]}" "$LABS_VM_USER@$gw" cat /var/lib/misc/gwdhcp.leases 2>/dev/null \
      | awk -v m="^($macs)$" 'tolower($2) ~ m {print $3; exit}')
    [[ -n "$ip" ]] && { echo "$ip"; return 0; }
  fi
  return 1
}

wait_vm_ip() {
  local vm=$1 timeout=${2:-180} ip t=0
  until ip=$(vm_ip "$vm"); do
    (( t >= timeout )) && return 1
    sleep 3; t=$((t + 3))
  done
  echo "$ip"
}

SSH_OPTS=(-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=5)

# Libère sur la passerelle core les baux DHCP de ces MAC (VM supprimée) : son IP redevient libre et son
# nom DNS disparaît tout de suite au lieu d'attendre l'expiration du bail. Sans passerelle : rien à faire.
core_release_leases() {
  (($#)) || return 0
  [[ $(vm_state core-gw 2>/dev/null) == running ]] || return 0
  local gw; gw=$(vm_ip core-gw) || return 0
  # shellcheck disable=SC2029  # MAC validées par gwctl, développées côté hôte volontairement
  ssh "${SSH_OPTS[@]}" "$LABS_VM_USER@$gw" sudo gwctl release "$@" >/dev/null 2>&1 \
    || warn "baux DHCP non libérés sur core-gw ($*) : ./lab.sh core dhcp release $*"
}

random_mac() { printf '52:54:00:%02x:%02x:%02x\n' $((RANDOM % 256)) $((RANDOM % 256)) $((RANDOM % 256)); }

# Adresse IP de l'hôte sur un réseau libvirt (= passerelle des VMs)
net_host_ip() { virsh net-dumpxml "$1" | grep -m1 -oP "<ip address='\K[^']+" || true; }
# Domaine DNS d'un réseau libvirt (vide si aucun)
net_domain() { virsh net-dumpxml "$1" | grep -m1 -oP "<domain name='\K[^']+" || true; }
# Domaine DNS des VMs d'un réseau : celui du module qui l'utilise (modules/*/module.env), sinon celui
# déclaré dans libvirt (réseaux créés par net-create.sh), sinon vide
net_lab_domain() {
  local env d
  for env in "$SCRIPTS_DIR"/../modules/*/module.env; do
    [[ -f $env ]] || continue
    # shellcheck source=/dev/null
    d=$( source "$env"; [[ ${NET:-} == "$1" ]] && echo "${DOMAIN:-$NET.$LABS_PKI_DOMAIN}" ) || true
    [[ -n $d ]] && { echo "$d"; return 0; }
  done
  net_domain "$1"
}
net_forward_mode() { virsh net-dumpxml "$1" | grep -m1 -oP "<forward mode='\K[^']+" || true; }

ip2int() { local IFS=.; read -r a b c d <<<"$1"; echo $(( (a << 24) + (b << 16) + (c << 8) + d )); }
int2ip() { echo "$(( ($1 >> 24) & 255 )).$(( ($1 >> 16) & 255 )).$(( ($1 >> 8) & 255 )).$(( $1 & 255 ))"; }
