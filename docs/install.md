# Installer labctl sur un autre poste

labctl est un **binaire autonome** : l'interface graphique et les fichiers de la passerelle (nftables,
DHCP/DNS, portail captif) sont embarqués dedans. Installer labctl ailleurs, c'est donc poser **un seul
fichier**, puis créer un *lab* (dossier qui contient la config et les modules). Rien à cloner, pas de Go,
pas de dépendance au dépôt source.

Le dépôt lui-même reste la façon de **développer** labctl (`source ./activate`, voir le README). Ce
document décrit la façon de le **distribuer et l'installer**.

## 1. Produire les artefacts (sur une machine de build)

```bash
labctl/build.sh release
```

Écrit dans `dist/` :

| Fichier | Rôle |
|---|---|
| `labctl` | binaire statique linux/amd64 (même archi que l'hôte et que la VM passerelle) |
| `labctl.sha256` | somme de contrôle du binaire |
| `install.sh` | installe le binaire sur le PATH + l'autocomplétion |
| `modules/<nom>.tar.gz` | une archive par module (état **commité** : ni fichiers non suivis, ni surcharges locales) |

On distribue ensuite ce qu'on veut : le binaire seul suffit à faire tourner labctl ; les
`modules/*.tar.gz` sont les labs tout prêts, à prendre à la carte.

## 2. Installer (sur le poste cible)

Prérequis de l'hôte : KVM/libvirt/QEMU et les outils réseau. `labctl check` les contrôle une fois installé.

```bash
sha256sum -c labctl.sha256     # vérifie le binaire
./install.sh                   # -> /usr/local/bin/labctl (+ complétion), sudo si nécessaire
```

Installation sans droits root, sous le home :

```bash
PREFIX="$HOME/.local" ./install.sh     # -> ~/.local/bin/labctl (vérifier que ~/.local/bin est dans le PATH)
```

`install.sh` pose aussi l'autocomplétion bash/zsh quand il trouve un dossier adéquat. Sinon, à la main :

```bash
labctl completion bash | sudo tee /usr/share/bash-completion/completions/labctl
```

## 3. Créer un lab et y déposer des modules

```bash
labctl check                           # prérequis de l'hôte
labctl init ~/mon-lab                  # config.env, lab.yaml, modules/ (vide)
cd ~/mon-lab

tar -xzf /chemin/modules/pjpt.tar.gz -C modules/   # déposer un module
labctl pki init                        # (optionnel) CA du lab : les VMs lui feront confiance
labctl pjpt up
```

Pour ajuster en un seul endroit ce qui change d'un poste à l'autre (chemins d'ISO, mot de passe,
domaine…), éditer **`lab.yaml`** à la racine du lab : ses variables surchargent celles des modules
(voir [module-yaml.md](module-yaml.md), section *Variables de niveau lab*). Les secrets propres au poste
vont dans `lab.local.yaml` (non versionné).

labctl retrouve le lab s'il est lancé **depuis son dossier** (ou un sous-dossier), ou via
`LABS_ROOT=/chemin/du/lab`.
