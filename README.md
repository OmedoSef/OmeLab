# OmeLab

Labs de VMs locaux sur **QEMU/KVM + libvirt**, organisés en **modules**.

```bash
source ./activate                 # active l'environnement (labs) : labctl + autocomplétion, dans ce shell seulement
labctl status                     # état de tout
labctl <module> <commande> ...    # TAB pour compléter commandes, VMs, modules, snapshots
labctl gui                        # interface graphique (navigateur, 127.0.0.1 uniquement)
deactivate
```

Sans activation : `bin/labctl <module> <commande> ...` (labctl retrouve le dossier Labs tout seul).
Voir [labctl](labctl/README.md) pour la GUI, la complétion et le débogage.

## Organisation

```
Labs/
├── activate          active l'environnement (labctl, complétion, Go du projet) ; deactivate pour sortir
├── config.env        réglages (stockage, utilisateur des VMs, clavier, domaine…) ; config.local.env pour les tiens
├── docs/             documentation : modules déclaratifs, passerelle core, VMs/réseaux/PKI
├── labctl/           outil de pilotage en Go : CLI avec complétion + GUI web locale
├── modules/
│   ├── attacker/     postes d'attaque : Kali, Parrot (branchés sur les labs cibles)
│   ├── pentest/      lab de pentest : Metasploitable 2, DVWA + Juice Shop
│   ├── pjpt/         lab Active Directory d'entraînement (préparation PJPT)
│   ├── sample/       exemple complet et commenté, à lire et à copier
│   └── netlab/       poste client pour tester la passerelle (pare-feu, portail, proxy)
├── pki/              PKI du lab (non versionnée : contient les clés privées)
├── bin/, .tools/     labctl compilé, Go et delve du projet (non versionnés)
└── .vscode/          configuration de débogage de labctl
```

| Module | Rôle | Doc |
|---|---|---|
| `core` | Passerelle Internet activable par module ou par VM, DHCP et DNS de tous les modules, liens entre modules | [docs/core.md](docs/core.md) |
| `attacker` | Postes d'attaque (Kali, Parrot), branchés sur les réseaux des labs cibles (`attach:`) | [modules/attacker](modules/attacker/README.md) |
| `pentest` | Lab d'entraînement au pentest, isolé | [modules/pentest](modules/pentest/README.md) |
| `pjpt` | Lab Active Directory d'entraînement (préparation PJPT), isolé | [modules/pjpt](modules/pjpt/README.md) |
| `sample` | Exemple complet et commenté, à lire et à copier | [modules/sample](modules/sample/README.md) |
| `netlab` | Poste client pour tester les fonctions de la passerelle (pare-feu, portail captif, proxy) | [modules/netlab](modules/netlab/README.md) |

VMs hors module, réseaux, images et PKI : [docs/vms.md](docs/vms.md).

## Repères de commandes

labctl tient en quelques familles. Au quotidien, **`labctl <module> …`** suffit presque toujours.

| Commande | Pour quoi | Niveau |
|---|---|---|
| `labctl <module> …` | piloter **un** module : `up`, `status`, `start`/`stop`, `snapshot`/`restore`, `attach`/`detach`, `destroy`… | **le plus courant** |
| `labctl module …` | les modules **en général** : `list`, `validate` (tous), `show <m>`, `edit <m>` ; `module <m> …` fait la même chose que `labctl <m> …` | déclaratif, transverse |
| `labctl lab …` | un **groupe de VMs** par étiquette libvirt (sans `module.yaml`) : `up`/`down`/`destroy`/`status`/`snapshot`/`revert` — surtout pour des VMs faites à la main (`vm create -l LAB`) | brut (libvirt) |
| `labctl vm …` | **une** VM : `create`, `destroy`, `ip`, `ssh`, `snapshot` | plus bas niveau |
| `labctl core …` / `labctl pki …` | la passerelle (Internet, DHCP, DNS, liens) / la PKI (CA, certificats) | infrastructure |

En clair :

- un **module** est la description déclarative (`module.yaml`) d'un lab — réseau isolé, CA, VMs, branchements. `labctl <module>` le construit et le gère : c'est ton outil principal.
- `labctl <module>` et `labctl module <module>` lancent **exactement les mêmes** commandes ; `module` ajoute seulement les actions transverses (lister, valider, montrer, éditer).
- un **lab** au sens `labctl lab` n'est qu'un groupe de VMs partageant une étiquette libvirt. Un module crée ses VMs dans un lab du même nom, mais `labctl lab` agit sur les VMs **brutes** : il ne crée ni réseau, ni CA, et ne vérifie pas l'isolation. À réserver aux VMs créées hors module.

## Principes communs

- **Chaque module a son réseau isolé** : un simple bridge libvirt sans forward, sans DHCP ni DNS. Il a aussi
  son sous-réseau `/24`, son domaine DNS et sa CA dérivée.
- **Tout est centralisé sur la passerelle `core-gw`**, présente en `.254` dans chaque module. Elle fournit le
  **DHCP** (IP fixes par MAC), le **DNS**, le **routage** et le **pare-feu**. Sans elle, les modules n'ont plus
  d'adresses : `labctl core start` d'abord.
- **Internet est fermé par défaut**, et toujours refermé au redémarrage de la passerelle :
  ```bash
  labctl core internet on attacker kali    # accès COMPLET, raccourci pour les VMs sans connexion dédiée
  labctl core internet off pentest
  ```
  C'est le gros interrupteur. Pour un accès **précis** (certains ports, un portail, un proxy), une VM déclare une
  `connections:` vers `internet` dans son `module.yaml` : elle **ouvre** ses ports par elle-même, sans cet
  interrupteur (voir [docs/module-yaml.md](docs/module-yaml.md)). Même ouverte, la passerelle ne laisse sortir que
  vers Internet. Ton LAN et les autres réseaux privés restent injoignables.
- **Liens entre modules** : par défaut, un module ne voit aucun autre module, ni par le routage ni par le DNS.
  `labctl core link A B [--both]` ouvre les deux d'un coup, dans un sens ou dans les deux.
- **DNS** : `<vm>.<domaine du module>`, des alias, et les noms Internet seulement pour les VMs autorisées à sortir.
- **Adressage** d'un module : `.1` = hôte, `.2`–`.99` = VMs à IP réservée, `.100`–`.199` = DHCP libre,
  `.254` = passerelle core (routeur, DHCP, DNS).
- **PKI** : racine *OmeLab Root CA* (organisation et domaine racine réglables : `LABS_PKI_ORG`,
  `LABS_PKI_DOMAIN`, choisis au `labctl init`). Chaque module a sa CA dérivée, limitée à son domaine et à son
  sous-réseau. Toutes les VMs créées par labctl font confiance à la racine.

## Démarrage

```bash
labctl check                   # prérequis de l'hôte
labctl pki init                # une fois : CA racine du lab
labctl core gw               # passerelle Internet + DNS
labctl pentest check
labctl pentest up              # réseau, CA, VMs : tout ce que décrit modules/pentest/module.yaml
```

Pour **installer labctl sur un autre poste** (binaire autonome, sans cloner le dépôt) puis créer un lab
avec `labctl init`, voir [docs/install.md](docs/install.md).

## Contrat d'un module

Un module est un dossier `modules/<nom>/` contenant :

| Fichier | Contenu |
|---|---|
| `module.yaml` | **la description du module** : réseau, CA, DNS (alias, réservations, liens), VMs — voir [docs/module-yaml.md](docs/module-yaml.md) |
| `files/` | fichiers copiés dans les VMs (configurations, modèles avec variables `${...}`) |
| `README.md` | exercices, notes |

Le moteur de labctl construit le module : `labctl <m> check|up|status|start|stop|snapshot|restore|certs|keyboard|ssh|attach|detach|destroy`
(aussi sous `labctl module <m> …` ; voir *Repères de commandes* plus haut). Aucune ligne de code propre à un module.
La passerelle `core` n'est pas un module : elle est intégrée à labctl (`labctl core …`, voir [docs/core.md](docs/core.md)).

`labctl module validate` contrôle les `module.yaml` sans rien créer ; `labctl module show <m>` affiche les valeurs
effectives de chaque VM et leur origine.


labctl découvre les modules automatiquement : un nouveau dossier avec un `module.yaml` apparaît dans `labctl`,
sa complétion et la GUI sans aucune modification du code Go ni recompilation.

## Licence

OmeLab est distribué sous la [licence Apache 2.0](LICENSE). Copyright 2026 Romain VOLPI et les contributeurs
d'OmeLab (voir [NOTICE](NOTICE) et [CONTRIBUTORS.md](CONTRIBUTORS.md) : chaque contributeur peut y ajouter son nom).
