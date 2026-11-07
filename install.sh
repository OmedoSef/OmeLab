#!/usr/bin/env sh
# Installe labctl sur le système : le binaire sur le PATH et l'autocomplétion du shell.
#
#   ./install.sh [CHEMIN_DU_BINAIRE]   installe labctl (défaut : ./labctl, à côté de ce script)
#   PREFIX=~/.local ./install.sh       installe sous un autre préfixe (défaut : /usr/local)
#
# Autonome : labctl embarque tout ce dont il a besoin (interface, fichiers de la passerelle). Après
# l'installation : « labctl init ~/mon-lab » crée un lab, puis on y dépose des modules.
set -eu

PREFIX="${PREFIX:-/usr/local}"
BINDIR="$PREFIX/bin"
HERE=$(unset CDPATH; cd "$(dirname "$0")" && pwd)
SRC="${1:-$HERE/labctl}"

[ -f "$SRC" ] || { echo "install.sh : binaire introuvable : $SRC (indiquer son chemin en argument)" >&2; exit 1; }

# sudo seulement si la cible n'est pas accessible en écriture (préfixe utilisateur : aucun sudo).
SUDO=""
if ! mkdir -p "$BINDIR" 2>/dev/null || [ ! -w "$BINDIR" ]; then
  if command -v sudo >/dev/null 2>&1; then
    SUDO="sudo"
    $SUDO mkdir -p "$BINDIR"
  else
    echo "install.sh : $BINDIR non accessible en écriture et sudo absent (essayer PREFIX=\$HOME/.local)" >&2
    exit 1
  fi
fi

$SUDO install -m 0755 "$SRC" "$BINDIR/labctl"
echo "ok  $BINDIR/labctl"

# Autocomplétion, au mieux : on pose ce que le binaire fraîchement installé sait générer, dans le
# premier dossier qui convient. Un échec reste silencieux et n'interrompt pas l'installation : la doc
# rappelle la commande manuelle (labctl completion bash|zsh).
LABCTL="$BINDIR/labctl"
install_completion() {
  shell=$1; out=$2; dir=$(dirname "$out")
  [ -d "$dir" ] || return 1
  if [ -w "$dir" ]; then
    "$LABCTL" completion "$shell" >"$out" 2>/dev/null && echo "ok  complétion $shell : $out"
  elif [ -n "$SUDO" ]; then
    "$LABCTL" completion "$shell" 2>/dev/null | $SUDO tee "$out" >/dev/null 2>&1 && echo "ok  complétion $shell : $out"
  else
    return 1 # pas de droit d'écriture et pas de sudo : on n'insiste pas
  fi
}
# Dossier utilisateur d'abord (installation sans sudo), puis dossiers système.
[ -n "${XDG_DATA_HOME:-}" ] && ubash="$XDG_DATA_HOME/bash-completion/completions" || ubash="$HOME/.local/share/bash-completion/completions"
[ -n "$SUDO" ] || mkdir -p "$ubash" 2>/dev/null || true
for d in "$ubash" /usr/share/bash-completion/completions /etc/bash_completion.d; do
  [ -d "$d" ] && install_completion bash "$d/labctl" && break
done
for d in /usr/share/zsh/vendor-completions /usr/share/zsh/site-functions; do
  [ -d "$d" ] && install_completion zsh "$d/_labctl" && break
done

cat <<EOF

labctl installé. Prochaines étapes :
  labctl check              # prérequis de l'hôte (KVM, libvirt, outils…)
  labctl init ~/mon-lab     # crée l'arborescence d'un lab
  cd ~/mon-lab              # puis déposer des modules dans modules/
EOF
