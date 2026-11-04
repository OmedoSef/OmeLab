#!/usr/bin/env bash
# Vérifie que l'hôte est prêt pour les labs (KVM, libvirt, outils, droits, clé SSH).
# shellcheck source=lib/common.sh
source "$(dirname "$0")/lib/common.sh"
fail=0
check() { if eval "$2" &>/dev/null; then ok "$1"; else warn "$1 -> $3"; fail=1; fi; }

check "virtualisation matérielle (vmx/svm)" "grep -Eq '(vmx|svm)' /proc/cpuinfo" "activer VT-x/AMD-V dans le BIOS"
check "/dev/kvm accessible" "[[ -r /dev/kvm && -w /dev/kvm ]]" "sudo usermod -aG kvm $USER"
check "groupe libvirt" "id -nG | grep -qw libvirt" "sudo usermod -aG libvirt $USER (puis reconnexion)"
for c in virsh virt-install qemu-img cloud-localds curl openssl; do
  check "commande $c" "command -v $c" "sudo apt install libvirt-clients virtinst qemu-utils cloud-image-utils curl openssl"
done
check "connexion à $LIBVIRT_DEFAULT_URI" "virsh uri" "sudo systemctl enable --now libvirtd"
check "réseau $LABS_DEFAULT_NETWORK actif" "net_active $LABS_DEFAULT_NETWORK" "virsh net-start $LABS_DEFAULT_NETWORK"
check "clé SSH $LABS_SSH_PUBKEY" "[[ -f $LABS_SSH_PUBKEY ]]" "ssh-keygen -t ed25519"
mkdir -p "$LABS_IMAGES_DIR" "$LABS_VM_DIR"
# qemu (utilisateur libvirt-qemu) doit pouvoir traverser chaque dossier parent du stockage
d=$LABS_VM_DIR
while [[ $d != / ]]; do
  check "libvirt-qemu peut traverser $d" "sudo -n -u libvirt-qemu test -x '$d' || getfacl -p '$d' 2>/dev/null | grep -Eq '^(user:libvirt-qemu|other)::?.?.x'" \
    "setfacl -m u:libvirt-qemu:x '$d'"
  d=$(dirname "$d")
done
if ((fail == 0)); then ok "hôte prêt"; else warn "des points sont à corriger"; exit 1; fi
