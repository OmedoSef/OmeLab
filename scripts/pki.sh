#!/usr/bin/env bash
# PKI du lab : CA racine -> CA intermédiaire -> certificats serveur/client.
#
#   pki.sh init                         crée la CA racine (20 ans) et l'intermédiaire (10 ans)
#   pki.sh sub-ca NOM -D DOMAINE [-s CIDR]
#                                       CA dérivée signée par la racine (10 ans), limitée (nameConstraints)
#                                       à DOMAINE et ses sous-domaines (+ au sous-réseau CIDR pour les IP)
#   pki.sh issue NOM [options]          émet un certificat signé par l'intermédiaire
#        -C, --ca SOUS-CA               signe avec une CA dérivée : domaine par défaut = le sien, et pas de
#                                       SAN au nom court (il serait refusé par la contrainte de nom)
#        -D, --domain DOM               domaine ajouté à NOM s'il n'a pas de point (défaut $LABS_PKI_DOMAIN)
#        -d, --dns a,b                  noms DNS supplémentaires, wildcards acceptés ('*.apps.k8s.omedo.lab')
#        -i, --ip x,y                   adresses IP (SAN)
#            --client                   certificat client (sinon serveur + client)
#            --days N                   validité (défaut 825 j, max accepté par Apple)
#            --pfx                      génère aussi un .p12 (mot de passe vide)
#   pki.sh list                         CA dérivées, certificats émis et dates d'expiration
#   pki.sh show NOM                     détail d'un certificat
#   pki.sh trust-vm VM...               installe la CA racine dans des VMs existantes (SSH)
#   pki.sh deploy VM NOM [DOSSIER]      copie clé + cert + chaîne de NOM dans la VM (défaut /etc/ssl/lab)
#   pki.sh trust-host                   installe la CA racine sur cet hôte (sudo)
#
# Les VMs créées par vm-create.sh après `pki.sh init` font déjà confiance à la CA racine.
# Fichiers dans $LABS_PKI_DIR : root/, intermediate/, sub-ca/NOM/, certs/NOM/{NOM.key,NOM.crt,NOM.fullchain.crt}
# Les clés privées ne sont pas chiffrées (usage lab) : ne pas versionner ce dossier.
# shellcheck source=lib/common.sh
source "$(dirname "$0")/lib/common.sh"
need_cmd openssl

ROOT_DIR="$LABS_PKI_DIR/root"
INT_DIR="$LABS_PKI_DIR/intermediate"
CERTS_DIR="$LABS_PKI_DIR/certs"
SUBCA_DIR="$LABS_PKI_DIR/sub-ca"
ROOT_CRT="$ROOT_DIR/root-ca.crt"; ROOT_KEY="$ROOT_DIR/root-ca.key"
INT_CRT="$INT_DIR/intermediate-ca.crt"; INT_KEY="$INT_DIR/intermediate-ca.key"
CHAIN="$INT_DIR/ca-chain.crt"   # intermédiaire + racine

usage() { sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }
serial() { echo "0x$(openssl rand -hex 16)"; }
require_ca() { [[ -f $INT_CRT && -f $INT_KEY ]] || die "PKI non initialisée : lancer pki.sh init"; }

cmd_init() {
  [[ -f $ROOT_CRT ]] && die "la CA racine existe déjà : $ROOT_CRT"
  mkdir -p "$ROOT_DIR" "$INT_DIR" "$CERTS_DIR"; chmod 700 "$LABS_PKI_DIR"
  local ext; ext=$(mktemp); trap 'rm -f "$ext" "$INT_DIR/intermediate-ca.csr"' RETURN

  info "CA racine"
  openssl genpkey -quiet -algorithm RSA -pkeyopt rsa_keygen_bits:4096 -out "$ROOT_KEY"
  openssl req -x509 -new -key "$ROOT_KEY" -sha256 -days 7300 -set_serial "$(serial)" \
    -subj "/O=$LABS_PKI_ORG/CN=$LABS_PKI_ORG Root CA" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -addext "subjectKeyIdentifier=hash" -out "$ROOT_CRT"

  info "CA intermédiaire"
  openssl genpkey -quiet -algorithm RSA -pkeyopt rsa_keygen_bits:4096 -out "$INT_KEY"
  openssl req -new -key "$INT_KEY" -subj "/O=$LABS_PKI_ORG/CN=$LABS_PKI_ORG Intermediate CA" -out "$INT_DIR/intermediate-ca.csr"
  cat >"$ext" <<EXT
basicConstraints=critical,CA:TRUE,pathlen:0
keyUsage=critical,keyCertSign,cRLSign
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid:always
EXT
  openssl x509 -req -in "$INT_DIR/intermediate-ca.csr" -CA "$ROOT_CRT" -CAkey "$ROOT_KEY" \
    -set_serial "$(serial)" -days 3650 -sha256 -extfile "$ext" -out "$INT_CRT" 2>/dev/null
  cat "$INT_CRT" "$ROOT_CRT" >"$CHAIN"
  chmod 600 "$ROOT_KEY" "$INT_KEY"
  openssl verify -CAfile "$ROOT_CRT" "$INT_CRT" >/dev/null
  ok "CA racine        : $ROOT_CRT"
  ok "CA intermédiaire : $INT_CRT"
  ok "chaîne           : $CHAIN"
}

cmd_sub_ca() {
  [[ -f $ROOT_KEY ]] || die "PKI non initialisée : lancer pki.sh init"
  local name='' domain='' cidr=''
  while (($#)); do
    case $1 in
      -D|--domain) domain=$2; shift ;;
      -s|--subnet) cidr=$2; shift ;;
      -*) die "option inconnue : $1" ;;
      *) name=$1 ;;
    esac; shift
  done
  [[ -n $name && -n $domain ]] || die "usage : pki.sh sub-ca NOM -D DOMAINE [-s CIDR]"
  [[ $name =~ ^[a-zA-Z0-9][a-zA-Z0-9-]*$ ]] || die "nom invalide : $name"
  local dir="$SUBCA_DIR/$name"
  if [[ -f $dir/ca.crt ]]; then ok "CA dérivée $name déjà présente ($(cat "$dir/domain"))"; return; fi
  mkdir -p "$dir"

  # Contraintes : DOMAINE et sous-domaines ; IP limitées au sous-réseau (ou interdites si aucun)
  local nc="permitted;DNS:$domain" net mask
  if [[ -n $cidr ]]; then
    [[ $cidr =~ ^([0-9]+\.){3}[0-9]+/([0-9]+)$ ]] || die "CIDR invalide : $cidr"
    mask=$(int2ip $(( (0xFFFFFFFF << (32 - ${cidr#*/})) & 0xFFFFFFFF )))
    net=$(int2ip $(( $(ip2int "${cidr%/*}") & $(ip2int "$mask") )))
    nc+=",permitted;IP:$net/$mask"
  else
    nc+=",excluded;IP:0.0.0.0/0.0.0.0"
  fi

  local ext; ext=$(mktemp); trap 'rm -f "$ext" "$dir/ca.csr"' RETURN
  cat >"$ext" <<EXT
basicConstraints=critical,CA:TRUE,pathlen:0
keyUsage=critical,keyCertSign,cRLSign
nameConstraints=critical,$nc
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid:always
EXT
  openssl genpkey -quiet -algorithm RSA -pkeyopt rsa_keygen_bits:4096 -out "$dir/ca.key"
  chmod 600 "$dir/ca.key"
  openssl req -new -key "$dir/ca.key" -subj "/O=$LABS_PKI_ORG/OU=$name/CN=$LABS_PKI_ORG $name CA" -out "$dir/ca.csr"
  openssl x509 -req -in "$dir/ca.csr" -CA "$ROOT_CRT" -CAkey "$ROOT_KEY" \
    -set_serial "$(serial)" -days 3650 -sha256 -extfile "$ext" -out "$dir/ca.crt" 2>/dev/null
  cat "$dir/ca.crt" "$ROOT_CRT" >"$dir/ca-chain.crt"
  echo "$domain" >"$dir/domain"
  openssl verify -CAfile "$ROOT_CRT" "$dir/ca.crt" >/dev/null
  ok "CA dérivée $name ($nc) -> $dir"
}

cmd_issue() {
  require_ca
  local name='' dns='' ips='' client=0 days=825 pfx=0 domain=$LABS_PKI_DOMAIN subca=''
  while (($#)); do
    case $1 in
      -d|--dns) dns=$2; shift ;;
      -D|--domain) domain=$2; shift ;;
      -C|--ca) subca=$2; shift ;;
      -i|--ip) ips=$2; shift ;;
      --client) client=1 ;;
      --days) days=$2; shift ;;
      --pfx) pfx=1 ;;
      -*) die "option inconnue : $1" ;;
      *) name=$1 ;;
    esac; shift
  done
  [[ -n $name ]] || usage 1
  [[ $name =~ ^[a-zA-Z0-9][a-zA-Z0-9.-]*$ ]] || die "nom invalide : $name (pour un wildcard : pki.sh issue apps -d '*.apps.$LABS_PKI_DOMAIN')"
  # CA émettrice : intermédiaire par défaut, ou CA dérivée (-C)
  local ca_crt=$INT_CRT ca_key=$INT_KEY bare=1
  if [[ -n $subca ]]; then
    [[ -f $SUBCA_DIR/$subca/ca.crt ]] || die "CA dérivée inconnue : $subca (pki.sh sub-ca)"
    ca_crt=$SUBCA_DIR/$subca/ca.crt; ca_key=$SUBCA_DIR/$subca/ca.key; bare=0
    [[ $domain == "$LABS_PKI_DOMAIN" ]] && domain=$(cat "$SUBCA_DIR/$subca/domain")
  fi
  local dir="$CERTS_DIR/$name"
  [[ -e $dir/$name.crt ]] && die "certificat déjà émis : $dir/$name.crt (supprimer le dossier pour le réémettre)"
  mkdir -p "$dir"

  # SAN : NOM (+ NOM.domaine si NOM n'est pas déjà un FQDN), puis les ajouts.
  # read -a plutôt qu'un for sur la chaîne : le shell développerait les '*' des wildcards.
  local san=() extra=() d i
  if [[ $name == *.* ]]; then san+=("DNS:$name")
  else ((bare)) && san+=("DNS:$name"); san+=("DNS:$name.$domain"); fi
  IFS=, read -ra extra <<<"$dns"; for d in "${extra[@]}"; do [[ -n $d ]] && san+=("DNS:$d"); done
  IFS=, read -ra extra <<<"$ips"; for i in "${extra[@]}"; do [[ -n $i ]] && san+=("IP:$i"); done
  local eku="serverAuth,clientAuth"; ((client)) && eku="clientAuth"

  local ext; ext=$(mktemp); trap 'rm -f "$ext" "$dir/$name.csr"' RETURN
  cat >"$ext" <<EXT
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=$eku
subjectAltName=$(IFS=,; echo "${san[*]}")
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid
EXT
  openssl genpkey -quiet -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$dir/$name.key"
  chmod 600 "$dir/$name.key"
  openssl req -new -key "$dir/$name.key" -subj "/O=$LABS_PKI_ORG/CN=$name" -out "$dir/$name.csr"
  openssl x509 -req -in "$dir/$name.csr" -CA "$ca_crt" -CAkey "$ca_key" \
    -set_serial "$(serial)" -days "$days" -sha256 -extfile "$ext" -out "$dir/$name.crt" 2>/dev/null
  cat "$dir/$name.crt" "$ca_crt" >"$dir/$name.fullchain.crt"
  openssl verify -CAfile "$ROOT_CRT" -untrusted "$ca_crt" "$dir/$name.crt" >/dev/null \
    || { rm -rf "$dir"; die "certificat refusé à la vérification (contrainte de nom de la CA ?)"; }
  if ((pfx)); then
    openssl pkcs12 -export -inkey "$dir/$name.key" -in "$dir/$name.crt" -certfile <(cat "$ca_crt" "$ROOT_CRT") \
      -passout pass: -out "$dir/$name.p12"
  fi
  ok "certificat $name ($(IFS=,; echo "${san[*]}")) -> $dir"
}

cmd_list() {
  local d
  for d in "$SUBCA_DIR"/*/; do
    [[ -f $d/ca.crt ]] || continue
    printf '%-30s %-26s %s\n' "[CA] $(basename "$d")" "$(openssl x509 -in "$d/ca.crt" -noout -enddate | cut -d= -f2)" "$(cat "$d/domain")"
  done
  printf '%-30s %-26s %s\n' NOM EXPIRE SAN
  local c
  for c in "$CERTS_DIR"/*/*.crt; do
    [[ -f $c && $c != *.fullchain.crt ]] || continue
    printf '%-30s %-26s %s\n' "$(basename "$c" .crt)" \
      "$(openssl x509 -in "$c" -noout -enddate | cut -d= -f2)" \
      "$(openssl x509 -in "$c" -noout -ext subjectAltName 2>/dev/null | tail -n +2 | xargs)"
  done
}

cmd_show() {
  local c="$CERTS_DIR/${1:?usage : pki.sh show NOM}/$1.crt"
  [[ -f $c ]] || die "certificat introuvable : $c"
  openssl x509 -in "$c" -noout -subject -issuer -dates -ext subjectAltName,extendedKeyUsage
}

# Script exécuté dans la VM : installe la CA lue sur stdin dans le magasin système (Debian/Ubuntu ou RHEL-like)
REMOTE_TRUST='set -e
if [ -d /usr/local/share/ca-certificates ]; then
  sudo tee /usr/local/share/ca-certificates/labs-root-ca.crt >/dev/null && sudo update-ca-certificates >/dev/null 2>&1
elif [ -d /etc/pki/ca-trust/source/anchors ]; then
  sudo tee /etc/pki/ca-trust/source/anchors/labs-root-ca.crt >/dev/null && sudo update-ca-trust
else echo "magasin de certificats non reconnu" >&2; exit 1; fi'

cmd_trust_vm() {
  [[ -f $ROOT_CRT ]] || die "PKI non initialisée : lancer pki.sh init"
  (($#)) || die "usage : pki.sh trust-vm VM..."
  local vm ip
  for vm; do
    ip=$(vm_ip "$vm") || { warn "$vm : pas d'IP, ignorée"; continue; }
    # shellcheck disable=SC2029  # script volontairement construit côté hôte
    ssh "${SSH_OPTS[@]}" "$LABS_VM_USER@$ip" "$REMOTE_TRUST" <"$ROOT_CRT" && ok "$vm fait confiance à la CA racine"
  done
}

cmd_deploy() {
  local vm=${1:?usage : pki.sh deploy VM NOM [DOSSIER]} name=${2:?usage : pki.sh deploy VM NOM [DOSSIER]} dest=${3:-/etc/ssl/lab}
  local dir="$CERTS_DIR/$name" ip
  [[ -f $dir/$name.crt ]] || die "certificat introuvable : $name (pki.sh issue $name)"
  ip=$(vm_ip "$vm") || die "pas d'IP pour $vm"
  # shellcheck disable=SC2029  # $dest et $name sont développés côté hôte, volontairement
  tar -C "$dir" -cf - "$name.key" "$name.crt" "$name.fullchain.crt" -C "$ROOT_DIR" root-ca.crt \
    | ssh "${SSH_OPTS[@]}" "$LABS_VM_USER@$ip" \
      "sudo mkdir -p '$dest' && sudo tar -C '$dest' --no-same-owner -xf - && sudo chmod 600 '$dest/$name.key'"
  ok "$name déployé sur $vm dans $dest"
}

cmd_trust_host() {
  [[ -f $ROOT_CRT ]] || die "PKI non initialisée : lancer pki.sh init"
  sudo cp "$ROOT_CRT" /usr/local/share/ca-certificates/labs-root-ca.crt
  sudo update-ca-certificates
  ok "CA racine installée sur l'hôte (navigateurs : importer $ROOT_CRT dans leur magasin si besoin)"
}

action=${1:-}; shift || true
case $action in
  init) cmd_init ;;
  sub-ca) cmd_sub_ca "$@" ;;
  issue) cmd_issue "$@" ;;
  list) cmd_list ;;
  show) cmd_show "$@" ;;
  trust-vm) cmd_trust_vm "$@" ;;
  deploy) cmd_deploy "$@" ;;
  trust-host) cmd_trust_host ;;
  -h|--help|"") usage ;;
  *) usage 1 ;;
esac
