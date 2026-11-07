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

Sur Debian/Ubuntu, le paquet `.deb` fait la même chose via le gestionnaire de paquets :

```bash
sudo apt install ./labctl_<version>_amd64.deb   # binaire + complétion ; désinstallation : apt remove labctl
```

Les artefacts (binaire, `install.sh`, archive prête à installer, `.deb`, une archive par module) sont
aussi publiés automatiquement sur la page **Releases** du dépôt GitHub, par le workflow `Release`
(`labctl/build.sh release` tourne en CI et pose le tag de version).

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

## 4. Mettre à jour labctl

Installer la nouvelle version comme la première (`./install.sh`, ou `sudo apt install ./labctl_<version>_amd64.deb`),
puis **mettre à jour la passerelle** :

```bash
labctl core update      # recopie labctl et les fichiers système sur la passerelle, puis applique
```

La passerelle garde sa propre copie de labctl (elle sert les portails captifs, `via: portal`). Cette copie est
rafraîchie à chaque démarrage de la passerelle et par `labctl core update` ; tant que ce n'est pas fait, hôte et
passerelle tournent sur deux versions différentes. `labctl check`, `labctl core status` et la page *Passerelle* de
la GUI le signalent (« labctl de la passerelle en version …, hôte en … »).

Les labs eux-mêmes (modules, `lab.yaml`, `network.local.yaml`, PKI) ne bougent pas lors d'une mise à jour.
Pour que l'éditeur suive les nouveautés de `module.yaml`, réécrire son schéma : `labctl module schema --write`.
