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

# Première IPv4 d'une VM (bail DHCP libvirt, agent invité, ARP, puis baux de la passerelle core) : labctl vm ip
vm_ip() { "$(labctl_bin)" vm ip "$1" 2>/dev/null; }

wait_vm_ip() {
  local vm=$1 timeout=${2:-180} ip t=0
  until ip=$(vm_ip "$vm"); do
    (( t >= timeout )) && return 1
    sleep 3; t=$((t + 3))
  done
  echo "$ip"
}

# Disposition de clavier des VMs : LABS_KEYBOARD, sinon celle de ce poste, sinon us
labs_keyboard() {
  local kb=${LABS_KEYBOARD:-}
  [[ -n $kb ]] || kb=$(localectl status 2>/dev/null | awk -F': *' '/X11 Layout/ {print $2; exit}' | cut -d, -f1)
  echo "${kb:-us}"
}

# Contenu de /etc/default/keyboard pour une disposition (cloud-init l'écrit tel quel : le module keyboard
# de cloud-init échoue sur les images sans console-setup, comme Debian)
keyboard_file() {
  printf 'XKBMODEL="pc105"\nXKBLAYOUT="%s"\nXKBVARIANT=""\nXKBOPTIONS=""\nBACKSPACE="guess"\n' "$1"
}

# Exécute une commande shell (root) dans une VM par l'agent invité QEMU ; affiche sa sortie, renvoie son code.
# La VM doit avoir le canal org.qemu.guest_agent.0 et qemu-guest-agent actif.
agent_exec() {
  local vm=$1 cmd=$2 json pid st t=0
  json=$(python3 -c 'import json,sys; print(json.dumps({"execute":"guest-exec","arguments":{"path":"/bin/sh","arg":["-c",sys.argv[1]],"capture-output":True}}))' "$cmd")
  pid=$(virsh qemu-agent-command "$vm" "$json" 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["return"]["pid"])') || return 125
  while ((t < 120)); do
    st=$(virsh qemu-agent-command "$vm" "{\"execute\":\"guest-exec-status\",\"arguments\":{\"pid\":$pid}}" 2>/dev/null) || return 125
    if python3 -c 'import json,sys; sys.exit(0 if json.loads(sys.argv[1])["return"]["exited"] else 1)' "$st"; then
      python3 -c '
import base64, json, sys
r = json.loads(sys.argv[1])["return"]
for k in ("out-data", "err-data"):
    if r.get(k): sys.stdout.write(base64.b64decode(r[k]).decode(errors="replace"))
sys.exit(r.get("exitcode", 1))' "$st"
      return
    fi
    sleep 1; t=$((t + 1))
  done
  return 124
}

# L'agent invité répond-il ?
agent_ready() { virsh qemu-agent-command "$1" '{"execute":"guest-ping"}' &>/dev/null; }

# Chemin de labctl (outil Go du projet), compilé au besoin avec le Go du projet ou dans Docker
labctl_bin() {
  local bin="$SCRIPTS_DIR/../bin/labctl"
  if [[ ! -x $bin ]]; then
    "$SCRIPTS_DIR/../labctl/build.sh" build >&2 || "$SCRIPTS_DIR/../labctl/build.sh" docker >&2 || die "labctl introuvable et impossible à compiler"
  fi
  echo "$bin"
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
    || warn "baux DHCP non libérés sur core-gw ($*) : labctl core dhcp release $*"
}

random_mac() { printf '52:54:00:%02x:%02x:%02x\n' $((RANDOM % 256)) $((RANDOM % 256)) $((RANDOM % 256)); }

# Adresse IP de l'hôte sur un réseau libvirt (= passerelle des VMs)
net_host_ip() { virsh net-dumpxml "$1" | grep -m1 -oP "<ip address='\K[^']+" || true; }
# Domaine DNS d'un réseau libvirt (vide si aucun)
net_domain() { virsh net-dumpxml "$1" | grep -m1 -oP "<domain name='\K[^']+" || true; }
net_forward_mode() { virsh net-dumpxml "$1" | grep -m1 -oP "<forward mode='\K[^']+" || true; }

ip2int() { local IFS=.; read -r a b c d <<<"$1"; echo $(( (a << 24) + (b << 16) + (c << 8) + d )); }
int2ip() { echo "$(( ($1 >> 24) & 255 )).$(( ($1 >> 16) & 255 )).$(( ($1 >> 8) & 255 )).$(( $1 & 255 ))"; }
