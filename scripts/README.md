# Scripts des labs

Outils pour monter des labs de VMs sur cet hôte avec **QEMU/KVM + libvirt** (`qemu:///system`).
Les VMs démarrent depuis des images cloud officielles (Debian, Ubuntu, Rocky, Alma) configurées par **cloud-init** :
utilisateur, clé SSH, IP, paquets et CA racine du lab sont posés au premier démarrage (~30 s).

## Démarrage rapide

```bash
./check-host.sh                      # vérifie KVM, libvirt, outils, droits
./pki.sh init                        # (une fois) CA racine + intermédiaire du lab
./net-create.sh demo -s 10.10.0.0/24 # réseau dédié au lab
./vm-create.sh web1 -l demo -n default -n demo:10.10.0.11/24 -w
./vm-ssh.sh web1
```

## Scripts

| Script | Rôle |
|---|---|
| `check-host.sh` | Vérifie que l'hôte est prêt |
| `image-get.sh` | Télécharge les images cloud (`--list` pour le catalogue) |
| `vm-create.sh` | Crée une VM (CPU, RAM, disque, cartes réseau, IP fixes, lab, paquets) |
| `vm-destroy.sh` | Supprime une VM et ses disques |
| `vm-list.sh` | VMs avec état, lab et IP |
| `vm-ip.sh` / `vm-ssh.sh` | IP d'une VM / SSH par nom |
| `vm-snapshot.sh` | Snapshots create / list / revert / delete |
| `net-create.sh` / `net-delete.sh` / `net-list.sh` | Réseaux virtuels (nat, isolated, route) |
| `lab.sh` | Actions groupées sur un lab : status, up, down, snapshot, revert, destroy |
| `pki.sh` | PKI du lab : CA racine → intermédiaire → certificats |

Chaque script affiche son aide avec `-h`.

## Conventions

- **Stockage** : images de base dans `~/Virtualisation/kvm/images`, une VM = un dossier
  `~/Virtualisation/kvm/vms/<nom>/` (disque qcow2 lié à l'image de base, ISO cloud-init, fichiers générés).
- **Accès** : utilisateur `lab`, sudo sans mot de passe, ta clé `~/.ssh/id_ed25519.pub`.
  Mot de passe `lab` utilisable seulement sur la console (`virsh console <vm>`), SSH par mot de passe désactivé.
- **Labs** : `vm-create.sh -l <lab>` étiquette la VM dans sa description libvirt (`managed-by=labs-scripts lab=<lab>`).
  `vm-destroy.sh` refuse de supprimer une VM sans cette étiquette (sauf `--force`) : les VMs existantes sont protégées.
- **Réseaux** : la 1re carte est la carte de gestion. `-n reseau` = DHCP, `-n reseau:IP/CIDR` = IP fixe.
  Sur un réseau créé par `net-create.sh`, l'hôte a l'adresse `.1` et le DHCP sert la 2e moitié de la plage
  (la 1re moitié est pour les IP fixes).
- **Noms DNS** : domaine racine `omedo.lab`. Chaque réseau a son sous-domaine (`net-create.sh k8s` → `k8s.omedo.lab`,
  modifiable avec `-D`, y compris sur plusieurs niveaux : `-D prod.k8s.omedo.lab`). Une VM prend le domaine de sa
  1re carte (`web1.k8s.omedo.lab`), ou `omedo.lab` sur le réseau `default`.
  Pour interconnecter des labs plus tard : réseaux en mode `route`, ou une VM routeur/pare-feu à plusieurs cartes.
- **Configuration** : valeurs par défaut dans `config.env`, à surcharger dans `config.local.env` (non versionné)
  ou par variable d'environnement.

## PKI du lab

```bash
./pki.sh init                                   # CA racine (20 ans) + intermédiaire (10 ans), dans ../pki/
./pki.sh issue web1 -D k8s.omedo.lab -i 10.10.0.11 # SAN : web1, web1.k8s.omedo.lab, IP
./pki.sh issue db1.data.prod.omedo.lab             # nom déjà complet : pris tel quel
./pki.sh issue ingress -D k8s.omedo.lab -d '*.apps.k8s.omedo.lab'   # wildcard (entre quotes)
./pki.sh issue alice --client --pfx             # cert client + .p12
./pki.sh deploy web1 web1                       # copie clé/cert/chaîne dans /etc/ssl/lab/ de la VM
./pki.sh trust-vm vieille-vm                    # installe la CA racine dans une VM existante
./pki.sh trust-host                             # fait confiance à la CA sur l'hôte
./pki.sh list
```

Les VMs créées **après** `pki.sh init` font automatiquement confiance à la CA racine (module `ca_certs` de cloud-init).
Pour un serveur, utiliser `<nom>.fullchain.crt` (certificat + CA émettrice).

**CA dérivées** : une CA par lab, signée par la racine et limitée à un domaine (+ un sous-réseau pour les IP) :

```bash
./pki.sh sub-ca pentest -D pentest-lab.omedo.lab -s 10.66.66.0/24
./pki.sh issue web -C pentest -i 10.66.66.30     # SAN : web.pentest-lab.omedo.lab, 10.66.66.30
```

Un certificat hors de ces limites est refusé à l'émission, et le serait aussi par les clients.
Les VMs font déjà confiance à la racine : rien à installer en plus.
Organisation : **Omedo Labs** (`Omedo Labs Root CA` → `Omedo Labs Intermediate CA`).
Un wildcard ne couvre qu'**un seul niveau** : `*.k8s.omedo.lab` couvre `a.k8s.omedo.lab` mais pas
`b.a.k8s.omedo.lab` ; ajouter un `-d` par niveau nécessaire.
Les clés privées ne sont pas chiffrées : le dossier `pki/` est exclu de git.
