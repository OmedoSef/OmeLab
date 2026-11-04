#!/usr/bin/env bash
# SSH vers une VM par son nom (sans polluer known_hosts, les IPs du lab étant réutilisées).
#   vm-ssh.sh VM                  shell interactif
#   vm-ssh.sh VM commande...      exécute une commande
#   vm-ssh.sh -u root VM          autre utilisateur que $LABS_VM_USER
# shellcheck source=lib/common.sh
source "$(dirname "$0")/lib/common.sh"
user=$LABS_VM_USER
[[ ${1:-} == -u ]] && { user=$2; shift 2; }
vm=${1:?usage : vm-ssh.sh [-u USER] VM [commande...]}; shift
vm_exists "$vm" || die "VM inconnue : $vm"
ip=$(wait_vm_ip "$vm" 60) || die "pas d'IP pour $vm"
# Juste après un démarrage ou un revert, sshd peut mettre quelques secondes à répondre
for _ in $(seq 20); do timeout 2 bash -c ">/dev/tcp/$ip/22" 2>/dev/null && break; sleep 2; done
exec ssh "${SSH_OPTS[@]}" "$user@$ip" "$@"
