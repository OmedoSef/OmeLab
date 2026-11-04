#!/usr/bin/env bash
# Règle la disposition du clavier dans des VMs existantes (console et interface graphique).
#   vm-keyboard.sh [-l DISPOSITION] VM...
#     -l, --layout X   disposition X11 (fr, us, be...) ; défaut : LABS_KEYBOARD, sinon celle de ce poste
# Passe par SSH (utilisateur $LABS_VM_USER) si possible, sinon par l'agent invité QEMU (VMs sans SSH
# par clé, ex. Kali). L'interface graphique prend le nouveau clavier à la prochaine ouverture de session.
# Les VMs créées par vm-create.sh ont déjà le bon clavier (cloud-init).
# shellcheck source=lib/common.sh
source "$(dirname "$0")/lib/common.sh"

layout=; vms=()
while (($#)); do
  case $1 in
    -l|--layout) layout=$2; shift ;;
    -h|--help) sed -n '2,8p' "$0"; exit 0 ;;
    *) vms+=("$1") ;;
  esac; shift
done
((${#vms[@]})) || die "usage : vm-keyboard.sh [-l DISPOSITION] VM..."
layout=${layout:-$(labs_keyboard)}
[[ $layout =~ ^[a-z]{2,3}(,[a-z]{2,3})*$ ]] || die "disposition invalide : $layout"

# Script exécuté en root dans la VM (Debian, Ubuntu, Kali...)
remote="set -e
# localectl peut être refusé (ex. via l'agent invité) : /etc/default/keyboard ci-dessous fait foi
if command -v localectl >/dev/null; then localectl set-x11-keymap $layout 2>/dev/null || true; fi
if [ -f /etc/default/keyboard ]; then
  sed -i 's/^XKBLAYOUT=.*/XKBLAYOUT=\"$layout\"/' /etc/default/keyboard
else
  printf 'XKBMODEL=\"pc105\"\nXKBLAYOUT=\"$layout\"\nXKBVARIANT=\"\"\nXKBOPTIONS=\"\"\n' > /etc/default/keyboard
fi
command -v setupcon >/dev/null && setupcon --force --save -k >/dev/null 2>&1 || true
grep XKBLAYOUT /etc/default/keyboard"

fail=0
for vm in "${vms[@]}"; do
  vm_exists "$vm" || { warn "$vm n'existe pas"; fail=1; continue; }
  [[ $(vm_state "$vm") == running ]] || { warn "$vm est arrêtée"; fail=1; continue; }
  ip=$(vm_ip "$vm" || true)
  if [[ -n $ip ]] && ssh "${SSH_OPTS[@]}" -o BatchMode=yes "$LABS_VM_USER@$ip" true 2>/dev/null; then
    how=SSH
    # Script envoyé sur l'entrée standard : pas de problème de guillemets
    out=$(ssh "${SSH_OPTS[@]}" "$LABS_VM_USER@$ip" sudo sh -s <<<"$remote" 2>&1) && rc=0 || rc=$?
  elif agent_ready "$vm"; then
    how="agent invité"
    out=$(agent_exec "$vm" "$remote" 2>&1) && rc=0 || rc=$?
  else
    warn "$vm : ni SSH par clé ni agent invité QEMU ; régler le clavier à la main dans la VM (ex. sudo localectl set-x11-keymap $layout)"
    fail=1; continue
  fi
  if ((rc == 0)); then
    ok "$vm : clavier $layout ($how) — $(grep -m1 XKBLAYOUT <<<"$out")"
  else
    warn "$vm : échec ($how) : $out"; fail=1
  fi
done
exit "$fail"
