#!/usr/bin/env bash
# Module core : services communs à tous les modules — passerelle Internet, DHCP, DNS et liens entre modules.
#
#   ./lab.sh core gw                          crée la passerelle core-gw et la branche sur chaque module
#   ./lab.sh core apply                       régénère et recharge réseau, DHCP, DNS et pare-feu de la passerelle
#   ./lab.sh core update                      pousse la version courante des fichiers système, puis apply
#   ./lab.sh core attach|detach MODULE        branche / débranche la passerelle du réseau d'un module
#
#   ./lab.sh core internet on MODULE [VM...]  ouvre Internet au module entier, ou seulement à ces VMs
#   ./lab.sh core internet off MODULE [VM...] referme (tout le module, ou seulement ces VMs)
#   ./lab.sh core internet status
#
#   ./lab.sh core link A B [--both]           A peut joindre B et résoudre ses noms (--both : et inversement)
#   ./lab.sh core unlink A B [--both]
#   ./lab.sh core links                       liens entre modules
#
#   ./lab.sh core dhcp list [MODULE]          réservations (IP fixes) des modules
#   ./lab.sh core dhcp leases [MODULE]        baux en cours
#   ./lab.sh core dhcp add MODULE NOM OCTET MAC   réserve SOUS-RESEAU.OCTET pour MAC, nommée NOM
#   ./lab.sh core dhcp del MODULE NOM
#   ./lab.sh core dhcp release MAC...         libère des baux (fait automatiquement à la suppression d'une VM)
#
#   ./lab.sh core dns list [MODULE]           noms et alias DNS
#   ./lab.sh core dns alias add MODULE NOM CIBLE   NOM.<domaine> -> CIBLE (VM du module ou nom complet)
#   ./lab.sh core dns alias del MODULE NOM
#
#   ./lab.sh core status | start | stop | ssh [cmd] | destroy
#
# Tout ce qui concerne un module côté core vit dans deux fichiers du module :
#   module.env  réseau, domaine, VMs du module avec IP et MAC (HOSTS, MACS), VMs sans Internet
#   core.conf   réservations DHCP en plus (host), alias DNS (alias), liens vers d'autres modules (link)
#
# Internet est fermé par défaut et après chaque redémarrage de la passerelle. Même ouverte, elle ne laisse
# sortir que vers Internet (jamais vers ton LAN ni les réseaux privés). Sans lien, aucun module ne voit
# un autre (ni routage, ni DNS). Les VMs NO_INTERNET n'initient rien hors de leur module.
# shellcheck disable=SC2031  # faux positif : load_module évalue la sortie d'un sous-shell
# shellcheck source=../../scripts/lib/common.sh
source "$(dirname "$0")/../../scripts/lib/common.sh"
# shellcheck source=../../scripts/lib/module.sh
source "$SCRIPTS_DIR/lib/module.sh"

HERE="$(cd "$(dirname "$0")" && pwd)"
GW=$CORE_GW_VM
DHCP_RANGE_START=100
DHCP_RANGE_END=199

usage() { sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

# ---------------------------------------------------------------- core.conf d'un module
conf_file() { echo "$MODULES_DIR/$1/core.conf"; }
# Lignes d'un type (host / alias / link) du core.conf d'un module, sans le mot-clé
conf_entries() { local f; f=$(conf_file "$1"); [[ -f $f ]] && awk -v t="$2" '$1==t {$1=""; sub(/^ /, ""); print}' "$f"; true; }
# conf_edit MODULE "ligne à ajouter" ["regex des lignes à retirer"]
conf_edit() {
  local f; f=$(conf_file "$1")
  [[ -f $f ]] || printf '# Module %s côté core (géré par ./lab.sh core dhcp|dns|link)\n# host NOM OCTET MAC  |  alias NOM CIBLE  |  link MODULE\n' "$1" >"$f"
  [[ -z ${3:-} ]] || { grep -vE "$3" "$f" >"$f.tmp" || true; mv "$f.tmp" "$f"; }
  [[ -z ${2:-} ]] || echo "$2" >>"$f"
}

# Charge le module.env (et les réservations de core.conf) d'un module dans des variables M_*
load_module() {
  local env; env=$(module_env "${1:?nom de module manquant}")
  [[ -f $env ]] || die "module inconnu : $1 ($(module_list | xargs))"
  M_MODULE='' M_NET='' M_SUBNET='' M_NO_INTERNET='' M_DOMAIN=''
  # shellcheck disable=SC2153  # MODULE, NET, SUBNET... viennent du module.env sourcé
  eval "$(
    # shellcheck source=/dev/null
    source "$env"
    printf 'M_MODULE=%q M_NET=%q M_SUBNET=%q M_NO_INTERNET=%q M_DOMAIN=%q\n' \
      "$MODULE" "$NET" "$SUBNET" "${NO_INTERNET:-}" "${DOMAIN:-$NET.$LABS_PKI_DOMAIN}"
    printf 'declare -gA M_HOSTS=('; for k in "${!HOSTS[@]}"; do printf '[%q]=%q ' "$k" "${HOSTS[$k]}"; done; echo ')'
    printf 'declare -gA M_MACS=('; for k in "${!MACS[@]}"; do printf '[%q]=%q ' "$k" "${MACS[$k]}"; done; echo ')'
  )"
  local name octet mac
  while read -r name octet mac; do
    [[ -n $name ]] || continue
    M_HOSTS["$name"]=$octet; M_MACS["$name"]=$mac
  done < <(conf_entries "$1" host)
}
host_ip() { [[ -n ${M_HOSTS[$1]:-} ]] || die "VM inconnue dans le module $M_MODULE : $1 (${!M_HOSTS[*]})"; echo "$M_SUBNET.${M_HOSTS[$1]}"; }

gw_running() { [[ $(vm_state "$GW" 2>/dev/null) == running ]]; }
require_gw() { gw_running || die "passerelle absente ou arrêtée (./lab.sh core gw / start)"; }
apply_if_running() { if gw_running; then core_apply; fi; }

# Modules dont la passerelle est branchée sur le réseau
attached_modules() {
  local m
  for m in $(module_list); do
    load_module "$m"
    virsh domiflist "$GW" 2>/dev/null | grep -qi "$(gw_mac "$M_SUBNET")" && echo "$m"
  done
  true
}

# ---------------------------------------------------------------- application de la configuration
# La configuration (réseau .254, DHCP, DNS, liens, VMs bloquées) est générée par labctl core-config
# (Go, labctl/internal/coreconf) depuis module.env et core.conf ; ce script l'envoie et recharge les services.
core_apply() {
  require_gw
  local tmp m t instances=(dhcp) attached=''
  tmp=$(mktemp -d)
  "$(labctl_bin)" core-config "$tmp" || { rm -rf "$tmp"; die "génération de la configuration impossible"; }
  # Modules branchés et instances DNS : lus dans la configuration générée
  while read -r _ _ _ m _; do
    [[ -n $m ]] || continue
    attached+="$m "; instances+=("$m-closed" "$m-open")
  done <"$tmp/gwdns/modules"
  attached=${attached% }

  tar -C "$tmp" -cf - . | core_gw_ssh "set -e
    t=\$(mktemp -d); tar -C \$t --no-same-owner -xf -
    sudo rm -rf /etc/gwdns && sudo mv \$t/gwdns /etc/gwdns && sudo chmod 755 /etc/gwdns
    sudo rm -f /etc/systemd/network/50-gw-*.network /etc/systemd/network/60-modules.network
    sudo cp \$t/networkd/* /etc/systemd/network/ 2>/dev/null || true
    rm -rf \$t
    sudo networkctl reload"
  rm -rf "$tmp"

  # Adresses .254 en place avant de (re)lancer DHCP et DNS
  for m in $attached; do
    load_module "$m"; t=0
    until core_gw_ssh "ip -4 -br addr | grep -qw '$M_SUBNET.$GW_OCTET/24'" 2>/dev/null; do
      ((t >= 30)) && die "la passerelle n'a pas pris $M_SUBNET.$GW_OCTET sur $M_NET"
      sleep 2; t=$((t + 2))
    done
  done
  core_gw_ssh "for u in \$(systemctl list-units --all --plain --no-legend 'gwdns@*' | awk '{print \$1}'); do
      i=\${u#gwdns@}; i=\${i%.service}; [ -f /etc/gwdns/\$i.conf ] || sudo systemctl disable -q --now \$u; done
    sudo systemctl stop gwdns@dhcp 2>/dev/null; sudo gwctl prune-leases
    for i in ${instances[*]}; do sudo systemctl enable -q gwdns@\$i && sudo systemctl restart gwdns@\$i; done
    sudo gwctl apply"
  ok "passerelle à jour : ${attached:-aucun module branché} (DHCP + $((${#instances[@]} - 1)) instances DNS)"
}

# ---------------------------------------------------------------- gw / update
GW_FILES=(
  "gwctl:/usr/local/sbin/gwctl:0755"
  "gwdhcp-hook:/usr/local/sbin/gwdhcp-hook:0755"
  "gw.service:/etc/systemd/system/gw.service:0644"
  "gwdns@.service:/etc/systemd/system/gwdns@.service:0644"
  "90-gw.conf:/etc/sysctl.d/90-gw.conf:0644"
)

cmd_gw() {
  if vm_exists "$GW"; then
    ok "$GW existe déjà"
  else
    local spec src dest mode files=()
    for spec in "${GW_FILES[@]}"; do IFS=: read -r src dest mode <<<"$spec"; files+=(-f "$HERE/files/$src:$dest:$mode"); done
    "$SCRIPTS_DIR/vm-create.sh" "$GW" -i debian13 -c 1 -m 512 -d 10 -n default -l core \
      -p nftables,dnsmasq,dnsmasq-utils "${files[@]}" \
      -r "sysctl --system >/dev/null" \
      -r "systemctl disable --now dnsmasq" \
      -r "systemctl daemon-reload && systemctl enable --now gw.service" \
      -r "systemctl enable systemd-networkd" \
      -w
  fi
  gw_running || virsh start "$GW" >/dev/null
  local m
  for m in $(module_list); do
    load_module "$m"
    if net_exists "$M_NET"; then cmd_attach "$m" noapply; else info "module $m : réseau $M_NET absent, branchement plus tard"; fi
  done
  core_apply
}

cmd_update() {
  require_gw
  local spec src dest mode
  core_gw_ssh "dpkg -s dnsmasq dnsmasq-utils >/dev/null 2>&1 || sudo DEBIAN_FRONTEND=noninteractive apt-get install -qq -y dnsmasq dnsmasq-utils >/dev/null"
  for spec in "${GW_FILES[@]}"; do
    IFS=: read -r src dest mode <<<"$spec"
    core_gw_ssh "sudo install -m $mode /dev/stdin $dest" <"$HERE/files/$src"
  done
  # Le service dnsmasq par défaut de Debian n'est pas utilisé : instances gwdns@ (dhcp, <module>-open/closed)
  core_gw_ssh "sudo systemctl disable -q --now dnsmasq; sudo systemctl daemon-reload && sudo /usr/sbin/sysctl -q --system"
  core_apply
}

# ---------------------------------------------------------------- attach / detach
cmd_attach() {
  load_module "${1:-}"
  vm_exists "$GW" || { info "pas de passerelle : ./lab.sh core gw (DHCP, DNS et Internet du module $M_MODULE)"; return 0; }
  net_exists "$M_NET" || die "réseau $M_NET absent (./lab.sh $M_MODULE network)"
  local mac; mac=$(gw_mac "$M_SUBNET")
  if virsh domiflist "$GW" | grep -qi "$mac"; then
    ok "passerelle déjà branchée sur $M_NET"
  else
    local live=(); gw_running && live=(--live)
    virsh attach-interface "$GW" network "$M_NET" --model virtio --mac "$mac" --config "${live[@]}" >/dev/null
    ok "passerelle branchée sur $M_NET"
  fi
  [[ ${2:-} == noapply ]] || apply_if_running
}

cmd_detach() {
  load_module "${1:-}"
  local mac; mac=$(gw_mac "$M_SUBNET")
  if ! vm_exists "$GW" || ! virsh domiflist "$GW" | grep -qi "$mac"; then ok "passerelle non branchée sur $M_NET"; return 0; fi
  gw_running && internet_off "$M_MODULE"
  local live=(); gw_running && live=(--live)
  virsh detach-interface "$GW" network --mac "$mac" --config "${live[@]}" >/dev/null
  ok "passerelle débranchée de $M_NET (le module n'a plus de DHCP, DNS ni Internet)"
  apply_if_running
}

# ---------------------------------------------------------------- internet
gwctl() { core_gw_ssh sudo gwctl "$@"; }

internet_on() {
  load_module "$1"; shift
  require_gw
  core_gw_ssh "ip -4 -br addr | grep -qw '$M_SUBNET.$GW_OCTET/24'" || die "passerelle non branchée sur $M_NET (./lab.sh core attach $M_MODULE)"
  local vm
  if (($#)); then
    for vm; do
      [[ " $M_NO_INTERNET " == *" $vm "* ]] && die "$vm est dans NO_INTERNET du module $M_MODULE : jamais d'accès Internet"
      gwctl allow "$(host_ip "$vm")"
    done
  else
    gwctl allow "$M_SUBNET.0/24"
  fi
  ok "Internet ouvert pour $M_MODULE${*:+ ($*)}${M_NO_INTERNET:+ ; toujours bloquées : $M_NO_INTERNET}"
}

internet_off() {
  load_module "$1"; shift
  require_gw
  local vm
  if (($#)); then
    for vm; do gwctl revoke "$(host_ip "$vm")"; done
  else
    gwctl revoke "$M_SUBNET.0/24"
    for vm in "${!M_HOSTS[@]}"; do gwctl revoke "$(host_ip "$vm")"; done
  fi
  ok "Internet fermé pour $M_MODULE${*:+ ($*)}"
}

internet_status() {
  local state m line allowed vm
  if ! gw_running; then warn "passerelle arrêtée ou absente : ni Internet, ni DHCP, ni DNS pour les modules"; return 0; fi
  state=$(gwctl status)
  printf '%-12s %-16s %s\n' MODULE RESEAU INTERNET
  for m in $(module_list); do
    load_module "$m"
    allowed=()
    while read -r line; do
      [[ $line == "allowed $M_SUBNET."* ]] || continue
      line=${line#allowed }
      if [[ $line == */24 ]]; then allowed=("tout le module"); break; fi
      for vm in "${!M_HOSTS[@]}"; do [[ $(host_ip "$vm") == "$line" ]] && allowed+=("$vm"); done
    done <<<"$state"
    if ! virsh domiflist "$GW" | grep -qi "$(gw_mac "$M_SUBNET")"; then
      printf '%-12s %-16s %s\n' "$m" "$M_NET" "non branché (./lab.sh core attach $m)"
    elif ((${#allowed[@]})); then
      printf '%-12s %-16s %s\n' "$m" "$M_NET" "OUVERT : ${allowed[*]}${M_NO_INTERNET:+ (jamais : $M_NO_INTERNET)}"
    else
      printf '%-12s %-16s %s\n' "$m" "$M_NET" "fermé"
    fi
  done
}

cmd_internet() {
  local action=${1:-}; shift || true
  case $action in
    on) [[ -n ${1:-} ]] || usage 1; internet_on "$@" ;;
    off) [[ -n ${1:-} ]] || usage 1; internet_off "$@" ;;
    status|"") internet_status ;;
    *) usage 1 ;;
  esac
}

# ---------------------------------------------------------------- liens entre modules
cmd_link() {
  local action=$1 a=${2:-} b=${3:-} both=0
  [[ ${4:-} == --both ]] && both=1
  [[ -n $a && -n $b ]] || usage 1
  load_module "$a"; load_module "$b"
  [[ $a != "$b" ]] || die "un module est toujours relié à lui-même"
  local pairs=("$a $b") pair src dst
  ((both)) && pairs+=("$b $a")
  for pair in "${pairs[@]}"; do
    read -r src dst <<<"$pair"
    conf_edit "$src" "" "^link $dst\$"
    if [[ $action == link ]]; then conf_edit "$src" "link $dst"; ok "lien $src -> $dst : $src joint et résout $dst"
    else ok "lien $src -> $dst retiré"; fi
  done
  apply_if_running
}

cmd_links() {
  local m dst any=0
  printf '%-12s %s\n' MODULE "PEUT JOINDRE"
  for m in $(module_list); do
    dst=$(conf_entries "$m" link | xargs)
    printf '%-12s %s\n' "$m" "${dst:-(aucun module : isolé)}"
    [[ -n $dst ]] && any=1
  done
  ((any)) || info "aucun lien : chaque module est isolé des autres (./lab.sh core link A B)"
}

# ---------------------------------------------------------------- dhcp
cmd_dhcp() {
  local action=${1:-list}; shift || true
  local m vm
  case $action in
    list)
      for m in ${1:-$(module_list)}; do
        load_module "$m"
        info "Module $m : $M_SUBNET.0/24, DHCP libre .$DHCP_RANGE_START-.$DHCP_RANGE_END, passerelle/DNS .$GW_OCTET"
        for vm in $(for k in "${!M_HOSTS[@]}"; do echo "${M_HOSTS[$k]} $k"; done | sort -n | awk '{print $2}'); do
          printf '  %-16s %-14s %s\n' "$(host_ip "$vm")" "$vm" "${M_MACS[$vm]:-(pas de MAC : DNS seulement)}"
        done
      done ;;
    leases)
      require_gw
      local filter=''
      [[ -n ${1:-} ]] && { load_module "$1"; filter="$M_SUBNET."; }
      printf '%-20s %-18s %-16s %s\n' EXPIRATION MAC IP NOM
      core_gw_ssh "cat /var/lib/misc/gwdhcp.leases 2>/dev/null" | while read -r exp mac ip name _; do
        [[ $ip == "$filter"* ]] && printf '%-20s %-18s %-16s %s\n' "$(date -d "@$exp" '+%F %T')" "$mac" "$ip" "$name"
      done; true ;;
    add)
      local name=${2:-} octet=${3:-} mac=${4:-}
      load_module "${1:-}"
      [[ $name =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ ]] || die "nom invalide : $name"
      if ! [[ $octet =~ ^[0-9]+$ ]] || ((octet < 2 || octet >= DHCP_RANGE_START)); then
        die "OCTET entre 2 et $((DHCP_RANGE_START - 1)) (hors plage DHCP libre)"
      fi
      [[ ${mac,,} =~ ^([0-9a-f]{2}:){5}[0-9a-f]{2}$ ]] || die "MAC invalide : $mac"
      [[ -z ${M_HOSTS[$name]:-} ]] || die "$name est déjà réservée (module.env ou core.conf)"
      for vm in "${!M_HOSTS[@]}"; do
        [[ ${M_HOSTS[$vm]} != "$octet" ]] || die "$M_SUBNET.$octet est déjà réservée pour $vm"
        [[ ${M_MACS[$vm]:-,} != "${mac,,}" ]] || die "MAC déjà réservée pour $vm"
      done
      conf_edit "$1" "host $name $octet ${mac,,}"
      ok "réservation $name : $M_SUBNET.$octet pour ${mac,,} (DNS : $name.$M_DOMAIN)"
      apply_if_running ;;
    release)
      (($#)) || usage 1
      require_gw
      core_gw_ssh sudo gwctl release "$@" ;;
    del)
      load_module "${1:-}"
      [[ -n $(conf_entries "$1" host | awk -v n="${2:-}" '$1==n') ]] || die "pas de réservation ${2:-} dans core.conf de $1 (celles de module.env se changent dans module.env)"
      conf_edit "$1" "" "^host ${2} "
      ok "réservation ${2} retirée"
      apply_if_running ;;
    *) usage 1 ;;
  esac
}

# ---------------------------------------------------------------- dns
cmd_dns() {
  local action=${1:-list}; shift || true
  local name_re='^[a-z0-9]([a-z0-9-]*[a-z0-9])?$' m name target
  case $action in
    list)
      for m in ${1:-$(module_list)}; do
        load_module "$m"
        info "Module $m : domaine $M_DOMAIN, DNS $M_SUBNET.$GW_OCTET"
        for name in $(printf '%s\n' "${!M_HOSTS[@]}" | sort); do printf '  %-34s %s\n' "$name.$M_DOMAIN" "$(host_ip "$name")"; done
        while read -r name target; do
          [[ -n $name ]] && printf '  %-34s -> %s\n' "$name.$M_DOMAIN" "$target"
        done < <(conf_entries "$m" alias)
        printf '  résout aussi : %s\n' "$(conf_entries "$m" link | xargs | sed 's/^$/(aucun autre module)/')"
      done ;;
    alias)
      local op=${1:-}; m=${2:-}; name=${3:-}; target=${4:-}
      load_module "$m"
      [[ $name =~ $name_re ]] || die "nom d'alias invalide : $name"
      case $op in
        add)
          [[ -n $target ]] || usage 1
          [[ $target == *.* || -n ${M_HOSTS[$target]:-} ]] || die "cible inconnue dans $m : $target (VM : ${!M_HOSTS[*]}, ou nom complet)"
          [[ -z ${M_HOSTS[$name]:-} ]] || die "$name est déjà le nom d'une VM"
          conf_edit "$m" "alias $name $target" "^alias $name "
          ok "alias $name.$M_DOMAIN -> $target" ;;
        del) conf_edit "$m" "" "^alias $name "; ok "alias $name.$M_DOMAIN retiré" ;;
        *) usage 1 ;;
      esac
      apply_if_running ;;
    *) usage 1 ;;
  esac
}

# ---------------------------------------------------------------- status
cmd_status() {
  if ! vm_exists "$GW"; then warn "passerelle absente (./lab.sh core gw)"; return 0; fi
  local st; st=$(vm_state "$GW")
  info "Passerelle $GW : $st${st:+, WAN $(core_gw_ip || echo '?')}"
  virsh domiflist "$GW" | awk 'NR>2 && NF {printf "  carte %-18s sur %s\n", $5, $3}'
  if gw_running; then
    core_gw_ssh "systemctl list-units --all --plain --no-legend 'gwdns@*'" | awk '{printf "  service %-28s %s\n", $1, $4}'
  fi
  echo
  internet_status
  echo
  cmd_links
}

action=${1:-}; shift || true
case $action in
  gw) cmd_gw ;;
  apply) core_apply ;;
  update) cmd_update ;;
  attach) cmd_attach "$@" ;;
  detach) cmd_detach "$@" ;;
  internet) cmd_internet "$@" ;;
  link|unlink) cmd_link "$action" "$@" ;;
  links) cmd_links ;;
  dhcp) cmd_dhcp "$@" ;;
  dns) cmd_dns "$@" ;;
  status) cmd_status ;;
  start) gw_running || virsh start "$GW" >/dev/null; ok "$GW démarrée (Internet fermé pour tous les modules)" ;;
  stop) virsh shutdown "$GW" >/dev/null; ok "$GW en cours d'arrêt : les modules n'ont plus de DHCP, DNS ni Internet" ;;
  ssh) core_gw_ssh "$@" ;;
  destroy) "$SCRIPTS_DIR/vm-destroy.sh" "$GW" ;;
  -h|--help|"") usage ;;
  *) usage 1 ;;
esac
