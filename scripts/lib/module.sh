# shellcheck shell=bash disable=SC2034  # NET_CHANGED, GW_OCTET... lus par les modules
# Briques communes aux modules (modules/<nom>/module.sh). À sourcer après lib/common.sh.
#
# Contrat d'un module : un fichier modules/<nom>/module.env qui définit au minimum
#   MODULE      nom du module
#   NET         réseau libvirt du module (isolé)
#   SUBNET      trois premiers octets du /24, ex : 10.66.66
#   HOSTS       tableau associatif  VM -> dernier octet de son IP réservée (entre 2 et 99)
#   MACS        tableau associatif  VM -> adresse MAC de sa carte (sert à la réservation DHCP)
#   NO_INTERNET VMs qui n'initient jamais rien hors du module (ni Internet, ni modules liés)
#   DOMAIN      (optionnel) domaine DNS du module, défaut : <NET>.$LABS_PKI_DOMAIN
# DHCP, DNS, routage et liens sont assurés par la passerelle du module core, toujours en .254.
# Le réseau libvirt n'est qu'un bridge isolé : l'hôte y a l'adresse .1, sans DHCP ni DNS.

MODULES_DIR="$(cd "$SCRIPTS_DIR/../modules" && pwd)"
GW_OCTET=254

module_env() { echo "$MODULES_DIR/$1/module.env"; }
module_list() { local f; for f in "$MODULES_DIR"/*/module.env; do basename "$(dirname "$f")"; done; }

# MAC de la carte de la passerelle sur le réseau d'un module : 52:54:00:fe:<2e octet>:<3e octet>
gw_mac() { local IFS=.; read -r _ b c <<<"$1"; printf '52:54:00:fe:%02x:%02x\n' "$b" "$c"; }

# Définit (ou met à jour) le réseau isolé d'un module : un bridge sans DHCP ni DNS libvirt
# (ils sont fournis par la passerelle core).
#   module_net_define NET BRIDGE SUBNET
# Positionne NET_CHANGED=1 si le réseau a été (re)défini, 0 s'il était déjà à jour.
NET_CHANGED=0
module_net_define() {
  local net=$1 bridge=$2 subnet=$3 xml uuid=''
  NET_CHANGED=0
  # Redéfinir un réseau existant exige de garder son UUID
  net_exists "$net" && uuid=$(virsh net-uuid "$net")
  xml=$(mktemp)
  {
    echo "<network>"
    echo "  <name>$net</name>"
    [[ -z $uuid ]] || echo "  <uuid>$uuid</uuid>"
    echo "  <bridge name='$bridge' stp='on' delay='0'/>"
    echo "  <dns enable='no'/>"
    echo "  <ip address='$subnet.1' netmask='255.255.255.0'/>"
    echo "</network>"
  } >"$xml"

  if net_exists "$net"; then
    [[ -z $(net_forward_mode "$net") ]] || { rm -f "$xml"; die "le réseau $net existe mais n'est PAS isolé"; }
    # Déjà à jour ? On compare à la définition EN SERVICE (une mise à jour peut être restée en attente),
    # sans les éléments générés par libvirt (uuid, mac, nombre de connexions)
    local cur new
    cur=$(virsh net-dumpxml "$net" | grep -vE '<uuid>|<mac address=' | sed -E "s/ connections='[0-9]+'//" | tr -d ' \n')
    new=$(grep -vE '<uuid>|<mac address=' "$xml" | tr -d ' \n')
    if [[ $cur == "$new" ]]; then rm -f "$xml"; ok "réseau $net à jour"; return 0; fi
  fi
  if ! virsh net-define "$xml" >/dev/null; then rm -f "$xml"; die "définition du réseau $net refusée par libvirt"; fi
  rm -f "$xml"
  NET_CHANGED=1
  ok "réseau $net défini ($subnet.0/24, isolé ; DHCP, DNS et passerelle : core-gw en .$GW_OCTET)"
}

# Démarre le réseau ; s'il tournait avec une ancienne définition, le redémarre si aucune VM ne l'utilise
module_net_start() {
  local net=$1 changed=$2 vm users=() gw_mac=''
  if net_active "$net"; then
    ((changed)) || return 0
    while read -r vm; do
      [[ -n $vm && $vm != "$CORE_GW_VM" ]] || continue
      virsh domiflist "$vm" | awk -v n="$net" '$3==n {f=1} END {exit !f}' && users+=("$vm")
    done < <(virsh list --name)
    if ((${#users[@]})); then
      warn "nouvelle définition de $net en attente : VMs démarrées dessus (${users[*]})."
      warn "l'appliquer : arrêter ces VMs puis relancer cette commande"
      return 0
    fi
    # La passerelle, elle, est débranchée à chaud le temps du redémarrage puis rebranchée
    if [[ $(vm_state "$CORE_GW_VM" 2>/dev/null) == running ]]; then
      gw_mac=$(virsh domiflist "$CORE_GW_VM" | awk -v n="$net" '$3==n {print $5; exit}')
      [[ -z $gw_mac ]] || virsh detach-interface "$CORE_GW_VM" network --mac "$gw_mac" --live >/dev/null
    fi
    virsh net-destroy "$net" >/dev/null
  fi
  virsh net-start "$net" >/dev/null
  virsh net-autostart "$net" >/dev/null
  [[ -z $gw_mac ]] || virsh attach-interface "$CORE_GW_VM" network "$net" --model virtio --mac "$gw_mac" --live >/dev/null
}

CORE_GW_VM=core-gw

# --- Stockage dans un pool libvirt dont le dossier appartient à root (pas de sudo : libvirt fait le travail)
pool_ensure() {
  local pool=$1 dir=$2
  if ! virsh pool-info "$pool" &>/dev/null; then
    virsh pool-define-as "$pool" dir --target "$dir" >/dev/null
    virsh pool-build "$pool" >/dev/null
    ok "pool $pool -> $dir"
  fi
  [[ $(virsh pool-info "$pool") == *State:*running* ]] || virsh pool-start "$pool" >/dev/null
  virsh pool-autostart "$pool" >/dev/null
}
vol_exists() { virsh vol-info --pool "$1" "$2" &>/dev/null; }
vol_upload() {
  local pool=$1 src=$2 vol=$3 fmt=$4
  vol_exists "$pool" "$vol" && return 0
  info "Envoi de $(basename "$src") dans le pool $pool ($(du -h "$src" | cut -f1))"
  virsh vol-create-as "$pool" "$vol" "$(stat -c %s "$src")" --format "$fmt" >/dev/null
  # Pas de --sparse : il multiplie les petits messages, sature max_client_requests de libvirtd et
  # la connexion tombe en keepalive timeout au bout de 30 s
  if ! virsh vol-upload --pool "$pool" "$vol" "$src"; then
    virsh vol-delete --pool "$pool" "$vol" >/dev/null 2>&1 || true
    die "échec de l'envoi de $(basename "$src") : volume $vol supprimé, relancer la commande"
  fi
  virsh pool-refresh "$pool" >/dev/null
  # Un envoi interrompu laisse un volume tronqué : on compare l'espace alloué à celui du fichier source
  local want got
  want=$(du -B1 "$src" | cut -f1)
  got=$(virsh vol-info --pool "$pool" "$vol" --bytes | awk '/^Allocation:/ {print $2}')
  if ((got < want * 95 / 100)); then
    virsh vol-delete --pool "$pool" "$vol" >/dev/null 2>&1 || true
    die "volume $vol incomplet ($got octets sur $want) : supprimé, relancer la commande"
  fi
}

# --- Téléchargements en cache (reprise possible) et vérification d'empreinte
fetch() {
  local url=$1 dst=$2
  [[ -f $dst ]] && return 0
  mkdir -p "$(dirname "$dst")"
  info "Téléchargement de $(basename "$dst")"
  curl -fL --progress-bar -C - -o "$dst.part" "$url"
  mv "$dst.part" "$dst"
}
verify_hash() {
  local algo=$1 expected=$2 file=$3 got
  [[ -n $expected ]] || die "empreinte $algo introuvable pour $(basename "$file")"
  info "Vérification $algo de $(basename "$file")"
  got=$("${algo}sum" "$file" | cut -d' ' -f1)
  [[ $got == "$expected" ]] || { rm -f "$file"; die "empreinte $algo invalide pour $file (fichier supprimé, relancer)"; }
  ok "empreinte $algo conforme"
}

# --- Accès à la passerelle du module core
core_gw_ip() { vm_exists "$CORE_GW_VM" && [[ $(vm_state "$CORE_GW_VM") == running ]] && vm_ip "$CORE_GW_VM"; }
core_gw_ssh() {
  local ip; ip=$(core_gw_ip) || die "passerelle $CORE_GW_VM absente ou arrêtée (./lab.sh core gw)"
  # shellcheck disable=SC2029  # commandes construites côté hôte, volontairement
  ssh "${SSH_OPTS[@]}" "$LABS_VM_USER@$ip" "$@"
}
