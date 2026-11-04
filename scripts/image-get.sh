#!/usr/bin/env bash
# Télécharge les images cloud de base dans $LABS_IMAGES_DIR.
#   image-get.sh --list             liste le catalogue et ce qui est déjà téléchargé
#   image-get.sh debian13 rocky9    télécharge ces images (ignorées si déjà présentes)
#   image-get.sh -f debian13        force le re-téléchargement (nouvelle version)
# shellcheck source=lib/common.sh
source "$(dirname "$0")/lib/common.sh"
# shellcheck source=lib/images.sh
source "$SCRIPTS_DIR/lib/images.sh"
need_cmd curl qemu-img

force=0; names=()
while (($#)); do
  case $1 in
    -l|--list)
      for n in $(printf '%s\n' "${!IMAGE_URL[@]}" | sort); do
        p=$(image_path "$n"); [[ -f $p ]] && s="présente ($(du -h "$p" | cut -f1))" || s="-"
        printf '%-12s %-20s %s\n' "$n" "$s" "${IMAGE_URL[$n]}"
      done; exit 0 ;;
    -f|--force) force=1 ;;
    -h|--help) sed -n '2,5p' "$0"; exit 0 ;;
    *) names+=("$1") ;;
  esac; shift
done
((${#names[@]})) || names=("$LABS_DEFAULT_IMAGE")

mkdir -p "$LABS_IMAGES_DIR"
for n in "${names[@]}"; do
  [[ -n "${IMAGE_URL[$n]:-}" ]] || die "image inconnue : $n (voir --list)"
  dst=$(image_path "$n")
  if [[ -f $dst && $force == 0 ]]; then ok "$n déjà présente : $dst"; continue; fi
  info "Téléchargement de $n"
  curl -fL --progress-bar -C - -o "$dst.part" "${IMAGE_URL[$n]}"
  qemu-img info "$dst.part" >/dev/null || die "image invalide : $dst.part"
  chmod 644 "$dst.part"; mv "$dst.part" "$dst"
  ok "$n -> $dst"
done
