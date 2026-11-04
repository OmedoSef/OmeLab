# Labs

Labs de VMs locaux sur **QEMU/KVM + libvirt**, organisés en **modules**.
Point d'entrée unique :

```bash
./lab.sh                          # liste les modules
./lab.sh <module>                 # aide du module
./lab.sh <module> <commande> ...  # exécute une commande
```

## Organisation

```
Labs/
├── lab.sh            point d'entrée
├── modules/
│   ├── core/         services communs : passerelle Internet, DHCP, DNS, liens (VM core-gw)
│   └── pentest/      lab de pentest : Kali, Metasploitable 2, DVWA + Juice Shop
├── scripts/          outils génériques (VMs, réseaux, images, PKI) et bibliothèques communes
└── pki/              PKI Omedo Labs (non versionnée : contient les clés privées)
```

| Module | Rôle | Doc |
|---|---|---|
| `core` | Passerelle Internet activable par module ou par VM, DHCP et DNS de tous les modules, liens entre modules | [modules/core](modules/core/README.md) |
| `pentest` | Lab d'entraînement au pentest, isolé | [modules/pentest](modules/pentest/README.md) |

Outils génériques et PKI : [scripts/README.md](scripts/README.md).

## Principes communs

- **Chaque module a son réseau isolé** : un simple bridge libvirt sans forward, sans DHCP ni DNS. Il a aussi
  son sous-réseau `/24`, son domaine DNS et sa CA dérivée.
- **Tout est centralisé sur la passerelle `core-gw`**, présente en `.254` dans chaque module. Elle fournit le
  **DHCP** (IP fixes par MAC), le **DNS**, le **routage** et le **pare-feu**. Sans elle, les modules n'ont plus
  d'adresses : `./lab.sh core start` d'abord.
- **Internet est fermé par défaut**, et toujours refermé au redémarrage de la passerelle :
  ```bash
  ./lab.sh core internet on pentest kali     # Kali seule
  ./lab.sh core internet off pentest
  ```
  Même ouverte, la passerelle ne laisse sortir que vers Internet. Ton LAN et les autres réseaux privés
  restent injoignables.
- **Liens entre modules** : par défaut, un module ne voit aucun autre module, ni par le routage ni par le DNS.
  `./lab.sh core link A B [--both]` ouvre les deux d'un coup, dans un sens ou dans les deux.
- **DNS** : `<vm>.<domaine du module>`, des alias, et les noms Internet seulement pour les VMs autorisées à sortir.
- **Adressage** d'un module : `.1` = hôte, `.2`–`.99` = VMs à IP réservée, `.100`–`.199` = DHCP libre,
  `.254` = passerelle core (routeur, DHCP, DNS).
- **PKI** : racine *Omedo Labs Root CA*. Chaque module a sa CA dérivée, limitée à son domaine et à son
  sous-réseau. Toutes les VMs créées par ces scripts font confiance à la racine.

## Démarrage

```bash
./scripts/pki.sh init          # une fois : CA racine Omedo Labs
./lab.sh core gw               # passerelle Internet + DNS
./lab.sh pentest check
./lab.sh pentest network       # réseau du module, branché automatiquement sur la passerelle
./lab.sh pentest kali          # puis msf2, web
```

## Contrat d'un module

Un module est un dossier `modules/<nom>/` contenant :

| Fichier | Contenu |
|---|---|
| `module.sh` | ses commandes ; la ligne 2 est sa description (affichée par `./lab.sh`) |
| `module.env` | `MODULE`, `NET`, `SUBNET`, `HOSTS` (VM → dernier octet), `MACS` (VM → MAC), `NO_INTERNET`, `DOMAIN` (optionnel) |
| `core.conf` | réservations DHCP en plus, alias DNS, liens vers d'autres modules (géré par `./lab.sh core dhcp/dns/link`) |

Les briques réseau, stockage, téléchargement et passerelle sont dans `scripts/lib/module.sh`.

À venir : une petite interface graphique pour piloter les modules. Elle s'appuiera sur ces mêmes commandes.
