# Labs

Labs de VMs locaux sur **QEMU/KVM + libvirt**, organisés en **modules**.

```bash
source ./activate                 # active l'environnement (labs) : labctl + autocomplétion, dans ce shell seulement
labctl status                     # état de tout
labctl <module> <commande> ...    # TAB pour compléter commandes, VMs, modules, snapshots
labctl gui                        # interface graphique (navigateur, 127.0.0.1 uniquement)
deactivate
```

Sans activation, le point d'entrée bash reste utilisable : `./lab.sh <module> <commande> ...`.
Voir [labctl](labctl/README.md) pour la GUI, la complétion et le débogage.

## Organisation

```
Labs/
├── activate          active l'environnement (labctl, complétion, Go du projet) ; deactivate pour sortir
├── lab.sh            point d'entrée bash
├── labctl/           outil de pilotage en Go : CLI avec complétion + GUI web locale
├── modules/
│   ├── core/         services communs : passerelle Internet, DHCP, DNS, liens (VM core-gw)
│   └── pentest/      lab de pentest : Kali, Metasploitable 2, DVWA + Juice Shop
├── scripts/          outils génériques (VMs, réseaux, images, PKI) et bibliothèques communes
├── pki/              PKI Omedo Labs (non versionnée : contient les clés privées)
├── bin/, .tools/     labctl compilé, Go et delve du projet (non versionnés)
└── .vscode/          configuration de débogage de labctl
```

| Module | Rôle | Doc |
|---|---|---|
| `core` | Passerelle Internet activable par module ou par VM, DHCP et DNS de tous les modules, liens entre modules | [docs/core.md](docs/core.md) |
| `pentest` | Lab d'entraînement au pentest, isolé | [modules/pentest](modules/pentest/README.md) |

Outils génériques et PKI : [scripts/README.md](scripts/README.md).

## Principes communs

- **Chaque module a son réseau isolé** : un simple bridge libvirt sans forward, sans DHCP ni DNS. Il a aussi
  son sous-réseau `/24`, son domaine DNS et sa CA dérivée.
- **Tout est centralisé sur la passerelle `core-gw`**, présente en `.254` dans chaque module. Elle fournit le
  **DHCP** (IP fixes par MAC), le **DNS**, le **routage** et le **pare-feu**. Sans elle, les modules n'ont plus
  d'adresses : `labctl core start` d'abord.
- **Internet est fermé par défaut**, et toujours refermé au redémarrage de la passerelle :
  ```bash
  labctl core internet on pentest kali     # Kali seule
  labctl core internet off pentest
  ```
  Même ouverte, la passerelle ne laisse sortir que vers Internet. Ton LAN et les autres réseaux privés
  restent injoignables.
- **Liens entre modules** : par défaut, un module ne voit aucun autre module, ni par le routage ni par le DNS.
  `labctl core link A B [--both]` ouvre les deux d'un coup, dans un sens ou dans les deux.
- **DNS** : `<vm>.<domaine du module>`, des alias, et les noms Internet seulement pour les VMs autorisées à sortir.
- **Adressage** d'un module : `.1` = hôte, `.2`–`.99` = VMs à IP réservée, `.100`–`.199` = DHCP libre,
  `.254` = passerelle core (routeur, DHCP, DNS).
- **PKI** : racine *Omedo Labs Root CA*. Chaque module a sa CA dérivée, limitée à son domaine et à son
  sous-réseau. Toutes les VMs créées par ces scripts font confiance à la racine.

## Démarrage

```bash
./scripts/pki.sh init          # une fois : CA racine Omedo Labs
labctl core gw               # passerelle Internet + DNS
labctl pentest check
labctl pentest up              # réseau, CA, VMs : tout ce que décrit modules/pentest/module.yaml
```

## Contrat d'un module

Un module est un dossier `modules/<nom>/` contenant :

| Fichier | Contenu |
|---|---|
| `module.yaml` | **la description du module** : réseau, CA, DNS (alias, réservations, liens), VMs — voir [docs/module-yaml.md](docs/module-yaml.md) |
| `files/` | fichiers copiés dans les VMs (configurations, modèles avec variables `${...}`) |
| `README.md` | exercices, notes |

Le moteur de labctl construit le module : `labctl <m> check|up|status|start|stop|snapshot|restore|certs|keyboard|ssh|destroy`
(aussi sous `labctl module <m> …` et `./lab.sh <m> …`). Aucune ligne de code propre à un module.
La passerelle `core` n'est pas un module : elle est intégrée à labctl (`labctl core …`, voir [docs/core.md](docs/core.md)).

`labctl module validate` contrôle les `module.yaml` sans rien créer ; `labctl module show <m>` affiche les valeurs
effectives de chaque VM et leur origine.


labctl découvre les modules automatiquement : un nouveau dossier avec un `module.yaml` apparaît dans `labctl`,
sa complétion et la GUI sans aucune modification du code Go ni recompilation.
