#!/usr/bin/env bash
# Crée une VM à partir d'une image cloud (disque qcow2 lié à l'image de base + cloud-init).
#
#   vm-create.sh NOM [options]
#     -i, --image IMG        image du catalogue (défaut : $LABS_DEFAULT_IMAGE, voir image-get.sh --list)
#     -c, --cpus N           vCPUs (défaut : $LABS_DEFAULT_CPUS)
#     -m, --memory MIB       RAM en MiB (défaut : $LABS_DEFAULT_MEMORY)
#     -d, --disk GIB         taille du disque en GiB (défaut : $LABS_DEFAULT_DISK)
#     -n, --net RESEAU[:IP/CIDR]
#                            carte réseau (répétable, la 1re est la carte de gestion).
#                            Sans IP : DHCP. Défaut : une carte sur $LABS_DEFAULT_NETWORK en DHCP.
#         --gw IP            passerelle de la 1re carte si IP fixe (défaut : IP hôte du réseau si NAT/route)
#         --dns IP[,IP]      serveurs DNS si IP fixe (défaut : la passerelle)
#     -l, --lab NOM          rattache la VM à un lab (voir lab.sh)
#     -p, --packages a,b     paquets supplémentaires à installer
#     -f, --file SRC:DEST[:MODE]  copie un fichier local dans la VM (répétable, MODE défaut 0644)
#     -r, --run CMD          commande shell exécutée en fin d'installation (répétable, dans l'ordre)
#         --user-data FICHIER  cloud-config personnalisé (remplace celui généré)
#         --no-ca            ne pas injecter la CA racine du lab
#     -w, --wait             attend la fin de cloud-init avant de rendre la main
#         --dry-run          affiche les fichiers générés sans créer la VM
#
# Exemples :
#   vm-create.sh web1 -l demo -w
#   vm-create.sh db1 -i rocky9 -m 4096 -n default -n lab-demo:10.10.0.11/24 -l demo
# shellcheck source=lib/common.sh
source "$(dirname "$0")/lib/common.sh"
# shellcheck source=lib/images.sh
source "$SCRIPTS_DIR/lib/images.sh"
need_cmd virsh virt-install qemu-img cloud-localds

usage() { sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

name=; image=$LABS_DEFAULT_IMAGE; cpus=$LABS_DEFAULT_CPUS; mem=$LABS_DEFAULT_MEMORY; disk=$LABS_DEFAULT_DISK
nets=(); gw=; dns=; lab=; packages=; user_data=; with_ca=1; wait=0; dry=0; files=(); runs=()
while (($#)); do
  case $1 in
    -i|--image) image=$2; shift ;;
    -c|--cpus) cpus=$2; shift ;;
    -m|--memory) mem=$2; shift ;;
    -d|--disk) disk=$2; shift ;;
    -n|--net) nets+=("$2"); shift ;;
    --gw) gw=$2; shift ;;
    --dns) dns=$2; shift ;;
    -l|--lab) lab=$2; shift ;;
    -p|--packages) packages=$2; shift ;;
    -f|--file) files+=("$2"); shift ;;
    -r|--run) runs+=("$2"); shift ;;
    --user-data) user_data=$2; shift ;;
    --no-ca) with_ca=0 ;;
    -w|--wait) wait=1 ;;
    --dry-run) dry=1 ;;
    -h|--help) usage ;;
    -*) die "option inconnue : $1" ;;
    *) [[ -z $name ]] || die "un seul nom de VM attendu"; name=$1 ;;
  esac; shift
done
[[ -n $name ]] || usage 1
[[ $name =~ ^[a-zA-Z0-9][a-zA-Z0-9-]*$ ]] || die "nom invalide (lettres, chiffres, tirets) : $name"
((${#nets[@]})) || nets=("$LABS_DEFAULT_NETWORK")
[[ -n "${IMAGE_URL[$image]:-}" ]] || die "image inconnue : $image (voir image-get.sh --list)"
[[ -z $user_data || -f $user_data ]] || die "fichier introuvable : $user_data"
for f in "${files[@]}"; do [[ -f ${f%%:*} ]] || die "fichier introuvable : ${f%%:*}"; done
((dry)) || ! vm_exists "$name" || die "la VM $name existe déjà"

base=$(image_path "$image")
[[ -f $base ]] || { ((dry)) || "$SCRIPTS_DIR/image-get.sh" "$image"; }
[[ -f $LABS_SSH_PUBKEY ]] || die "clé SSH publique introuvable : $LABS_SSH_PUBKEY"

vmdir="$LABS_VM_DIR/$name"
workdir=$vmdir; ((dry)) && workdir=$(mktemp -d)
mkdir -p "$workdir"

# --- Réseau : une entrée netplan v2 par carte, identifiée par son adresse MAC
netcfg="$workdir/network-config"
echo -e "version: 2\nethernets:" >"$netcfg"
net_args=()
for i in "${!nets[@]}"; do
  net=${nets[$i]%%:*}; addr=; [[ ${nets[$i]} == *:* ]] && addr=${nets[$i]#*:}
  ((dry)) || net_exists "$net" || die "réseau libvirt inexistant : $net (voir net-create.sh)"
  mac=$(random_mac)
  net_args+=(--network "network=$net,model=virtio,mac=$mac")
  {
    echo "  eth$i:"
    echo "    match: {macaddress: \"$mac\"}"
    echo "    set-name: eth$i"
    if [[ -z $addr ]]; then
      echo "    dhcp4: true"
      echo "    dhcp4-overrides: {use-domains: true}"
    else
      [[ $addr == */* ]] || die "IP fixe sans masque (ex : 10.10.0.11/24) : $addr"
      echo "    addresses: [$addr]"
      if ((i == 0)); then
        if [[ -z $gw ]] && ! ((dry)) && [[ $(net_forward_mode "$net") =~ ^(nat|route)$ ]]; then gw=$(net_host_ip "$net"); fi
        [[ -n $gw ]] && echo -e "    routes:\n      - {to: default, via: $gw}"
        d=${dns:-$gw}; [[ -n $d ]] && echo "    nameservers: {addresses: [${d//,/, }]}"
      fi
    fi
  } >>"$netcfg"
done

# --- cloud-init. FQDN : domaine du réseau de la 1re carte, sinon domaine racine du lab
domain=; ((dry)) || domain=$(net_lab_domain "${nets[0]%%:*}")
fqdn="$name.${domain:-$LABS_PKI_DOMAIN}"
echo -e "instance-id: $name-$(date +%s)\nlocal-hostname: $name" >"$workdir/meta-data"
if [[ -n $user_data ]]; then
  cp "$user_data" "$workdir/user-data"
else
  pkgs="qemu-guest-agent${packages:+,$packages}"
  {
    echo "#cloud-config"
    echo "hostname: $name"
    echo "fqdn: $fqdn"
    echo "manage_etc_hosts: true"
    echo "users:"
    echo "  - name: $LABS_VM_USER"
    echo "    shell: /bin/bash"
    echo "    sudo: ALL=(ALL) NOPASSWD:ALL"
    echo "    lock_passwd: false"
    echo "    plain_text_passwd: $LABS_VM_PASSWORD"
    echo "    ssh_authorized_keys:"
    echo "      - $(cat "$LABS_SSH_PUBKEY")"
    echo "ssh_pwauth: false"
    echo "packages: [${pkgs//,/, }]"
    echo "runcmd:"
    echo "  - [systemctl, enable, --now, qemu-guest-agent]"
    echo "  - [sh, -c, \"localectl set-x11-keymap $(labs_keyboard) 2>/dev/null || true\"]"
    for r in "${runs[@]}"; do printf '  - [sh, -c, %s]\n' "$(printf '%s' "$r" | sed 's/"/\\"/g; s/.*/"&"/')"; done
    # Clavier : /etc/default/keyboard (console et X), puis localectl ci-dessus
    echo "write_files:"
    echo "  - path: /etc/default/keyboard"
    echo "    encoding: b64"
    echo "    content: $(keyboard_file "$(labs_keyboard)" | base64 -w0)"
    if ((${#files[@]})); then
      for f in "${files[@]}"; do
        IFS=: read -r src dest mode <<<"$f"
        echo "  - path: $dest"
        echo "    permissions: '${mode:-0644}'"
        echo "    encoding: b64"
        echo "    content: $(base64 -w0 "$src")"
      done
    fi
    root_ca="$LABS_PKI_DIR/root/root-ca.crt"
    if ((with_ca)) && [[ -f $root_ca ]]; then
      echo "ca_certs:"
      echo "  trusted:"
      echo "    - |"
      sed 's/^/      /' "$root_ca"
    fi
  } >"$workdir/user-data"
fi

if ((dry)); then
  for f in user-data network-config meta-data; do echo "----- $f"; cat "$workdir/$f"; done
  echo "----- virt-install"; echo "${net_args[*]}"
  rm -rf "$workdir"; exit 0
fi

# --- Disques : overlay qcow2 sur l'image de base + ISO cloud-init
info "Création de $name ($image, ${cpus} vCPU, ${mem} MiB, ${disk} GiB)"
qemu-img create -q -f qcow2 -F qcow2 -b "$base" "$vmdir/disk.qcow2" "${disk}G"
cloud-localds -N "$netcfg" "$vmdir/seed.iso" "$vmdir/user-data" "$vmdir/meta-data"

osinfo=${IMAGE_OSINFO[$image]}
virt-install --osinfo list | grep -w "$osinfo" >/dev/null || osinfo=linux2022

virt-install --name "$name" \
  --memory "$mem" --vcpus "$cpus" --cpu host-passthrough \
  --osinfo "$osinfo" --import \
  --disk "path=$vmdir/disk.qcow2,format=qcow2,bus=virtio" \
  --disk "path=$vmdir/seed.iso,device=cdrom" \
  "${net_args[@]}" \
  --console pty,target_type=serial \
  --metadata "description=$LABS_MARKER${lab:+ lab=$lab}" \
  --noautoconsole >/dev/null
ok "VM $fqdn démarrée${lab:+ (lab $lab)}"

if ((wait)); then
  info "Attente de l'adresse IP..."
  ip=$(wait_vm_ip "$name" 300) || die "pas d'IP au bout de 5 min (console : virsh console $name)"
  info "IP $ip, attente de cloud-init..."
  for _ in $(seq 60); do ssh "${SSH_OPTS[@]}" "$LABS_VM_USER@$ip" true 2>/dev/null && break; sleep 3; done
  ssh "${SSH_OPTS[@]}" "$LABS_VM_USER@$ip" "cloud-init status --wait >/dev/null; cloud-init status" || warn "cloud-init a signalé une erreur"
  ok "$name prête : ssh $LABS_VM_USER@$ip  (ou vm-ssh.sh $name)"
fi
