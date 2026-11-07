#!/usr/bin/env bash
# Compile labctl dans Labs/bin/labctl.
#
#   labctl/build.sh            avec le Go du projet (.tools/go, voir activate)
#   labctl/build.sh docker     dans le conteneur officiel golang (aucun Go local nécessaire)
#   labctl/build.sh test       go vet + tests
#   labctl/build.sh tools      installe Go et delve dans .tools/ (téléchargement vérifié)
#   labctl/build.sh release    artefacts distribuables dans dist/ : binaire linux/amd64 statique +
#                              SHA256, install.sh, et une archive par module (état commité)
#
# Tout reste dans le projet : Go, caches et outils sous .tools/, binaire sous bin/ (exclus de git).
set -euo pipefail
LABS="$(cd "$(dirname "$0")/.." && pwd)"
SRC="$LABS/labctl"
TOOLS="$LABS/.tools"
GO_VERSION=1.27.3
GO_SHA256=65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40
GO_IMAGE=golang:1.27-bookworm

version() { git -C "$LABS" describe --always --dirty 2>/dev/null || echo dev; }
LDFLAGS="-s -w -X labs/labctl/internal/cli.Version=$(version)"

go_env() {
  export PATH="$TOOLS/go/bin:$TOOLS/bin:$PATH"
  export GOPATH="$TOOLS/gopath" GOMODCACHE="$TOOLS/gopath/pkg/mod" GOCACHE="$TOOLS/cache" GOBIN="$TOOLS/bin"
  export GOTOOLCHAIN=local GOTELEMETRY=off GOFLAGS=-modcacherw CGO_ENABLED=0
}

# go_ready prépare un Go utilisable : celui du projet (.tools/go) s'il est là, sinon un « go » du PATH
# (cas de la CI, où actions/setup-go le fournit). Échoue si aucun des deux.
go_ready() {
  if [[ -x $TOOLS/go/bin/go ]]; then
    go_env
  elif command -v go >/dev/null 2>&1; then
    export GOTOOLCHAIN=local GOTELEMETRY=off GOFLAGS=-modcacherw CGO_ENABLED=0
  else
    echo "Go introuvable (ni $TOOLS/go, ni \$PATH) : labctl/build.sh tools, ou installer Go" >&2
    exit 1
  fi
}

# build_deb construit un paquet Debian à partir du binaire déjà compilé. Sans outil tiers : dpkg-deb
# (présent sur Debian/Ubuntu et sur les runners GitHub). Absent → on saute, sans échouer le release.
build_deb() {
  local v=$1 bin=$2 distdir=$3
  if ! command -v dpkg-deb >/dev/null 2>&1; then
    echo "    (.deb non construit : dpkg-deb absent)"; return 0
  fi
  local debver=${v#v}; debver=${debver//-/+}         # deb n'aime ni le préfixe v ni le tiret (séparateur de révision)
  [[ $debver =~ ^[0-9] ]] || debver="0.0.0+$debver"  # une version .deb doit commencer par un chiffre (builds hors tag)
  local out="$distdir/labctl_${debver}_amd64.deb"
  local root; root=$(mktemp -d); chmod 0755 "$root" # sinon la racine du paquet hérite du 0700 de mktemp
  mkdir -p "$root/DEBIAN" "$root/usr/bin" "$root/usr/share/labctl" \
    "$root/usr/share/bash-completion/completions" "$root/usr/share/zsh/vendor-completions" \
    "$root/usr/share/doc/labctl"
  install -m0755 "$bin" "$root/usr/bin/labctl"
  "$bin" completion bash >"$root/usr/share/bash-completion/completions/labctl" 2>/dev/null || true
  "$bin" completion zsh >"$root/usr/share/zsh/vendor-completions/_labctl" 2>/dev/null || true
  [[ -f $LABS/docs/install.md ]] && cp "$LABS/docs/install.md" "$root/usr/share/doc/labctl/"
  cp "$LABS/LICENSE" "$LABS/NOTICE" "$root/usr/share/doc/labctl/"   # Apache-2.0 : licence et notice voyagent avec le binaire
  install -m0644 "$SRC/internal/module/module.schema.json" "$root/usr/share/labctl/" # schéma de module.yaml (éditeurs)
  cat >"$root/DEBIAN/control" <<EOF
Package: labctl
Version: $debver
Architecture: amd64
Maintainer: OmeLab <noreply@omelab.internal>
Section: admin
Priority: optional
Recommends: libvirt-clients, libvirt-daemon-system, qemu-system-x86, openssh-client, conntrack, gpgv
Description: Pilote de labs de virtualisation (QEMU/KVM, libvirt)
 labctl cree et pilote des labs de machines virtuelles declaratifs (reseaux,
 passerelle DHCP/DNS/pare-feu, PKI, modules). Binaire autonome ; « labctl init »
 cree un lab pret a recevoir des modules.
EOF
  dpkg-deb --build --root-owner-group "$root" "$out" >/dev/null
  rm -rf "$root"
  echo "    paquet -> $(basename "$out")"
}

case ${1:-build} in
  build)
    [[ -x $TOOLS/go/bin/go ]] || { echo "Go absent de $TOOLS : labctl/build.sh tools (ou labctl/build.sh docker)" >&2; exit 1; }
    go_env
    mkdir -p "$LABS/bin"
    (cd "$SRC" && go build -trimpath -ldflags "$LDFLAGS" -o "$LABS/bin/labctl" ./cmd/labctl)
    echo "ok  $LABS/bin/labctl ($(version))" ;;
  docker)
    mkdir -p "$LABS/bin" "$TOOLS/gopath" "$TOOLS/cache"
    docker run --rm -u "$(id -u):$(id -g)" \
      -v "$SRC:/src" -v "$LABS/bin:/out" -v "$TOOLS/gopath:/gopath" -v "$TOOLS/cache:/cache" \
      -e GOPATH=/gopath -e GOMODCACHE=/gopath/pkg/mod -e GOCACHE=/cache -e GOFLAGS=-modcacherw \
      -e GOTOOLCHAIN=local -e GOTELEMETRY=off -e CGO_ENABLED=0 -e HOME=/tmp -w /src \
      "$GO_IMAGE" go build -trimpath -ldflags "$LDFLAGS" -o /out/labctl ./cmd/labctl
    echo "ok  $LABS/bin/labctl (compilé dans $GO_IMAGE)" ;;
  release)
    go_ready
    v=$(version)
    dist="$LABS/dist"
    rm -rf "$dist"; mkdir -p "$dist/modules"
    # Binaire statique linux/amd64 (comme l'hôte et la VM passerelle qui en reçoit une copie)
    (cd "$SRC" && GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$LDFLAGS" -o "$dist/labctl" ./cmd/labctl)
    (cd "$dist" && sha256sum labctl >labctl.sha256)
    cp "$LABS/install.sh" "$LABS/LICENSE" "$LABS/NOTICE" "$SRC/internal/module/module.schema.json" "$dist/"
    # Archive « prête à installer » : binaire + installeur, avec la licence (Apache-2.0) et le schéma de module.yaml
    tar -czf "$dist/labctl_${v}_linux-amd64.tar.gz" -C "$dist" labctl install.sh LICENSE NOTICE module.schema.json
    echo "    archive -> labctl_${v}_linux-amd64.tar.gz"
    # Paquet Debian (binaire + complétion + doc)
    build_deb "$v" "$dist/labctl" "$dist"
    # Une archive par module, depuis l'état commité (git archive) : pas de fichiers non suivis ni locaux
    while read -r p; do
      [[ -n $p ]] || continue
      name=$(basename "$p")
      git -C "$LABS" archive --format=tar.gz -o "$dist/modules/$name.tar.gz" --prefix="$name/" \
        --add-file="$LABS/LICENSE" --add-file="$LABS/NOTICE" "HEAD:$p"
      echo "    module $name -> dist/modules/$name.tar.gz"
    done < <(git -C "$LABS" ls-tree --name-only HEAD modules/)
    echo "ok  dist/ : labctl ($v) + SHA256, install.sh, archive, .deb, $(find "$dist/modules" -name '*.tar.gz' | wc -l) module(s)" ;;
  test)
    go_ready # Go du projet, sinon celui du PATH (CI)
    (cd "$SRC" && go vet ./... && go test ./...) ;;
  tools)
    dl=$(mktemp -d); trap 'rm -rf "$dl"' EXIT
    f=go$GO_VERSION.linux-amd64.tar.gz
    curl -fsSL -o "$dl/$f" "https://go.dev/dl/$f"
    echo "$GO_SHA256  $dl/$f" | sha256sum -c --quiet
    mkdir -p "$TOOLS"; rm -rf "$TOOLS/go"; tar -C "$TOOLS" -xzf "$dl/$f"
    go_env
    go install github.com/go-delve/delve/cmd/dlv@latest
    echo "ok  Go $GO_VERSION et delve dans $TOOLS" ;;
  *) sed -n '2,9p' "$0"; exit 2 ;;
esac
