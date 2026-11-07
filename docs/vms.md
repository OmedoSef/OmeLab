# VMs, réseaux, images et PKI

Outils de labctl pour des VMs hors module, sur **QEMU/KVM + libvirt** (`qemu:///system`).
Les VMs démarrent depuis des images cloud officielles (Debian, Ubuntu, Rocky, Alma) configurées par **cloud-init** :
utilisateur, clé SSH, IP, paquets et CA racine des labs sont posés au premier démarrage (~30 s).

## Démarrage rapide

```bash
labctl check                                    # vérifie KVM, libvirt, outils, droits
labctl pki init                                 # (une fois) CA racine + intermédiaire
labctl net create demo -s 10.10.0.0/24          # réseau dédié
labctl vm create web1 -l demo -n default -n demo:10.10.0.11/24 -w
labctl vm ssh web1
```

## Commandes

| Commande | Rôle |
|---|---|
| `labctl check` | vérifie que l'hôte est prêt (KVM, libvirt, outils, clé SSH, accès de qemu au stockage) |
| `labctl image list` / `get NOM` | catalogue des images cloud ; téléchargement à empreinte vérifiée |
| `labctl vm create` | crée une VM (CPU, RAM, disque, cartes réseau, IP fixes, lab, paquets, fichiers) |
| `labctl vm destroy` | supprime une VM et ses disques |
| `labctl vm list` / `ip` / `ssh` | VMs avec état, lab et IP ; adresse d'une VM ; shell SSH |
| `labctl vm snapshot` | create / list / revert / delete |
| `labctl vm keyboard VM... [-l fr]` | clavier de VMs existantes (par SSH, ou par l'agent invité QEMU) |
| `labctl net list` / `create` / `delete` | réseaux virtuels (nat, isolated, route) |
| `labctl lab list` / `status` / `up` / `down` / `snapshot` / `revert` / `destroy` LAB | actions groupées sur les VMs d'un lab (`vm create -l LAB`) |
| `labctl pki …` | PKI des labs (voir plus bas) |

Chaque commande a son aide : `labctl <commande> --help`.

## Conventions

- **Stockage** : images de base dans `~/Virtualisation/kvm/images`, une VM = un dossier
  `~/Virtualisation/kvm/vms/<nom>/` (disque qcow2 lié à l'image de base, ISO cloud-init, fichiers générés).
- **Accès** : utilisateur `lab`, sudo sans mot de passe, ta clé `~/.ssh/id_ed25519.pub`.
  Mot de passe `lab` utilisable seulement sur la console (`virsh console <vm>`), SSH par mot de passe désactivé.
- **Labs** : `vm create -l <lab>` étiquette la VM dans sa description libvirt (`managed-by=labs-scripts lab=<lab>`).
  `vm destroy` refuse de supprimer une VM sans cette étiquette (sauf `--force`) : les VMs existantes sont protégées.
- **Réseaux** : la 1re carte est la carte de gestion. `-n reseau` = DHCP, `-n reseau:IP/CIDR` = IP fixe.
  Sur un réseau créé par `labctl net create`, l'hôte a l'adresse `.1` et le DHCP sert la 2e moitié de la plage
  (la 1re moitié est pour les IP fixes).
- **Noms DNS** : domaine racine `omelab.internal`. Chaque réseau a son sous-domaine (`net create k8s` → `k8s.omelab.internal`,
  modifiable avec `-D`, y compris sur plusieurs niveaux : `-D prod.k8s.omelab.internal`). Une VM prend le domaine de sa
  1re carte (`web1.k8s.omelab.internal`), ou `omelab.internal` sur le réseau `default`.
- **Clavier** : celui de ce poste (lu avec `localectl`), ou `LABS_KEYBOARD=us` (fr, be, ch…) dans `config.local.env`.
  Posé par cloud-init dès le premier démarrage ; pour une VM existante : `labctl vm keyboard VM...`.
- **Réglages** : valeurs par défaut dans [config.env](../config.env) (racine de Labs), à surcharger dans
  `config.local.env` (non versionné, `VAR=valeur`) ou par variable d'environnement.

## PKI des labs

```bash
labctl pki init                                   # CA racine (20 ans) + intermédiaire (10 ans), dans pki/
labctl pki issue web1 -D k8s.omelab.internal -i 10.10.0.11 # SAN : web1, web1.k8s.omelab.internal, IP
labctl pki issue db1.data.prod.omelab.internal             # nom déjà complet : pris tel quel
labctl pki issue ingress -D k8s.omelab.internal -d '*.apps.k8s.omelab.internal'   # wildcard (entre quotes)
labctl pki issue alice --client --pfx             # cert client + .p12
labctl pki deploy web1 web1                       # copie clé/cert/chaîne dans /etc/ssl/lab/ de la VM
labctl pki trust-vm vieille-vm                    # installe la CA racine dans une VM existante
labctl pki trust-host                             # fait confiance à la CA sur l'hôte
labctl pki list
```

Les VMs créées **après** `labctl pki init` font automatiquement confiance à la CA racine (module `ca_certs` de cloud-init).
Pour un serveur, utiliser `<nom>.fullchain.crt` (certificat + CA émettrice).

**CA dérivées** : une CA par module, signée par la racine et limitée à un domaine (+ un sous-réseau pour les IP).
Les modules la créent eux-mêmes (`pki: {ca: …}` dans leur `module.yaml`) ; à la main :

```bash
labctl pki sub-ca pentest -D pentest.omelab.internal -s 10.66.66.0/24
labctl pki issue web -C pentest -i 10.66.66.30     # SAN : web.pentest.omelab.internal, 10.66.66.30
```

Un certificat hors de ces limites est refusé à l'émission, et le serait aussi par les clients.
Organisation : **OmeLab** par défaut (`OmeLab Root CA` → `OmeLab Intermediate CA`), réglable par `LABS_PKI_ORG`
(choisie au `labctl init`, à fixer avant `labctl pki init`).
Un wildcard ne couvre qu'**un seul niveau** : `*.k8s.omelab.internal` couvre `a.k8s.omelab.internal` mais pas
`b.a.k8s.omelab.internal` ; ajouter un `-d` par niveau nécessaire.
Les clés privées ne sont pas chiffrées : le dossier `pki/` est exclu de git.

> ⚠️ **Sécurité de la PKI.** `pki/` (droits 0700, clés en 0600) contient en clair la clé de la CA racine et de
> l'intermédiaire. Qui peut lire ce dossier peut signer un certificat **pour n'importe quel nom** : la racine n'a pas
> de contrainte de nom (elle signe aussi les CA des modules, dont le domaine peut être hors du domaine racine,
> ex. `pjpt.local`).
> - Dans les VMs, c'est sans conséquence au-delà du lab : elles ne font confiance à la racine que pour le lab.
> - **Sur ton poste**, `labctl pki trust-host` ajoute cette racine aux autorités du système : quiconque lit `pki/`
>   pourrait alors intercepter le HTTPS de **ton poste** (banque, messagerie…). Ne le fais que si tu en as besoin
>   (consulter les services du lab depuis l'hôte sans alerte), et retire-la quand ce n'est plus le cas :
>   `sudo rm /usr/local/share/ca-certificates/labs-root-ca.crt && sudo update-ca-certificates --fresh`.
> - Un module ne peut pas copier un fichier de `pki/` dans une VM (`files: src` est confiné au module) ; seule
>   l'autorité d'interception du proxy part sur la passerelle.
> - Sauvegardes et copies de `pki/` : à traiter comme des secrets.
