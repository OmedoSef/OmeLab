# labctl

Outil de pilotage des labs, en Go : une **CLI avec autocomplétion** et une **GUI web locale**.
Les scripts bash des modules restent la source de vérité. labctl les découvre, lit leur configuration
et les appelle ; la logique sera portée en Go progressivement, module par module.

## Utilisation

```bash
source ./activate          # depuis Labs/ : (labs) dans l'invite, labctl + complétion disponibles
labctl status              # état de la passerelle, des modules et des VMs
labctl pentest restore <TAB>          # kali msf2 web
labctl pentest restore msf2 <TAB>     # clean (snapshots existants)
labctl core internet on pentest <TAB> # kali msf2 web
labctl pki issue srv -C <TAB>         # pentest (CA dérivées)
labctl gui                 # ouvre l'interface dans le navigateur
labctl hostfw status       # pare-feu de l'hôte face aux VMs des modules (on/off : avec sudo)
labctl vm list             # toutes les VMs : état, lab, IP ; labctl vm ip VM ; labctl vm snapshot ...
labctl net list            # réseaux libvirt ; labctl net create|delete
labctl -v pentest status   # -v : affiche la commande bash réellement lancée
deactivate                 # retire labctl, Go, la complétion et (labs)
```

`labctl <module> ...` est exactement `./lab.sh <module> ...`. La complétion est **tirée de l'en-tête d'aide de
chaque `module.sh`** (les lignes `#   ./lab.sh <module> commande ARG [OPT]`). Ajouter une commande à un module,
avec sa ligne d'aide, suffit à la rendre complétable, sans toucher au Go. Noms d'arguments reconnus :

| Dans l'aide | Complété par |
|---|---|
| `MODULE`, `A`, `B`, `AUTRE` | les modules qui ont un réseau |
| `VM`, `VM...` | les VMs du module courant, ou du module saisi juste avant |
| `NOM` juste après `VM` | les snapshots de cette VM |
| `--option` | l'option elle-même |

## Renouveler la chaîne de certificats

Rare, mais possible depuis la GUI (onglet PKI, *Maintenance de la chaîne*) ou en ligne de commande :

| Commande | Effet | Après |
|---|---|---|
| `labctl pki renew intermediate` | nouvelle intermédiaire (même racine) ; ses certificats réémis | rien sur les VMs ; redéployer les certificats réémis |
| `labctl pki renew sub-ca NOM` | nouvelle CA dérivée, mêmes limites ; ses certificats réémis | idem |
| `labctl pki renew root [--trust-vms]` | toute la chaîne recréée, tous les certificats réémis | installer la nouvelle racine sur les VMs (`--trust-vms` le fait sur les VMs démarrées joignables en SSH) et redéployer les certificats |

- Nouvelles clés partout ; les certificats réémis gardent leurs noms, leurs usages et leur durée de validité.
- La PKI est **archivée avant chaque opération** dans `pki/archive/<date>/` (`labctl pki archives`) ; retour arrière :
  recopier les fichiers de l'archive dans `pki/`.
- La racine demande de taper `RACINE` (ou `--yes` en ligne de commande).
- Les certificats installés par d'autres moyens que `labctl pki deploy` (ex. le HTTPS de la VM web du module pentest,
  posé par cloud-init dans `/opt/targets/certs`) sont à remettre en place à la main après un renouvellement.

## Migration des scripts bash vers Go

Chaque bloc porté en Go devient une commande de labctl, testée ; le script bash correspondant devient un
relais vers labctl (même syntaxe), puis disparaît quand plus rien ne l'appelle.

| Bloc | État | Remplace |
|---|---|---|
| PKI | ✅ en Go : `labctl pki` (`internal/pki`, crypto/x509, sans openssl) | `scripts/pki.sh` → relais |
| Génération de la config core (DHCP, DNS, pare-feu, liens) | ✅ en Go : `labctl core-config` (`internal/coreconf`) ; `core apply` l'appelle puis l'envoie | fonctions `gen_*` supprimées de `modules/core/module.sh` |
| Outils VM et réseaux | ✅ en Go : `labctl vm list|ip|snapshot`, `labctl net list|create|delete` (`internal/virt`, libvirt sans virsh) | `vm-list.sh`, `vm-ip.sh`, `vm-snapshot.sh`, `net-*.sh` → relais ; `vm_ip` des scripts passe par labctl |
| Images cloud (`image-get`) | à faire (avec la création de VMs) | `scripts/image-get.sh` |
| Création / suppression de VMs (cloud-init) | à faire | `vm-create.sh`, `vm-destroy.sh` |
| Édition de `core.conf` (réservations, alias, liens) | ✅ en Go (`internal/lab/corefile.go`), utilisée par la GUI | les commandes bash `core dhcp/dns/link` restent, même format |
| Modules `core`, `pentest` | à faire | `modules/*/module.sh` |

`labctl core-config --diff` montre ce qui changerait sur la passerelle (comme le bandeau de la GUI) ;
`labctl core-config` sans argument affiche la configuration qui serait appliquée sur la passerelle : pratique pour
contrôler l'effet d'un changement de `module.env` / `core.conf` avant `labctl core apply`. Au portage, la sortie Go
a été comparée fichier par fichier à celle du bash déployée sur la passerelle : identique.

La PKI Go est compatible avec les fichiers produits par l'ancien script (clés PKCS#8, mêmes chemins) ; un test
vérifie l'interopérabilité avec openssl dans les deux sens.

## GUI

`labctl gui` sert l'interface sur **127.0.0.1:8077** uniquement (`-p` pour un autre port, `--no-browser` pour ne
pas ouvrir le navigateur). Trois onglets :

- **Modules** : par module, état des VMs, IP, snapshots, accès Internet (module entier ou VM par VM) ; par VM :
  démarrer, arrêter, console (virt-viewer), snapshot, restauration ; champ pour lancer n'importe quelle commande
  d'un module.
- **Configuration** : par module, réservations DHCP (nom, IP, MAC), alias DNS et liens entre modules
  (`core.conf`). Les modifications sont enregistrées tout de suite mais **appliquées seulement à la demande** :
  un bandeau signale les changements en attente, montre exactement ce qui changera dans la configuration de la
  passerelle (diff), et le bouton *Appliquer* la déploie. Mêmes validations que la ligne de commande
  (IP ou MAC déjà prise, nom de VM, cible d'alias inconnue…). Les réservations de `module.env` sont affichées
  en lecture seule.
- **Core** : passerelle (créer, démarrer, arrêter, *Appliquer la configuration*, *Mettre à jour*, console),
  état des services (`gw`, `gwdns@dhcp`, DNS ouvert/fermé de chaque module), baux DHCP, sources autorisées et
  bloquées dans le pare-feu.
- **PKI** : CA racine, intermédiaire et CA dérivées avec leurs limites (domaine, sous-réseau) et leur expiration ;
  certificats émis avec leurs noms ; création d'une CA dérivée, émission d'un certificat, déploiement sur une
  VM, installation de la CA racine dans une VM ; téléchargement des **certificats publics** (jamais des clés
  privées ni des `.p12`, qui restent dans `pki/`) ; **maintenance de la chaîne** (renouveler l'intermédiaire, une
  CA dérivée, ou toute la chaîne : voir ci-dessous). `pki trust-host` (sudo) reste en ligne de commande.

La sortie des commandes s'affiche en direct dans le journal en bas de page.

**Sécurité** : seul ton poste peut s'y connecter (127.0.0.1). Chaque lancement tire un jeton aléatoire, placé
dans l'adresse ouverte (`#t=...`) et exigé pour chaque action, ce qui empêche un autre site web ouvert dans le
navigateur de piloter tes labs. L'en-tête `Host` est vérifié contre le *DNS rebinding*. Une seule action tourne
à la fois, et seules les commandes des modules existants et de la PKI sont acceptées. La suppression d'un module entier
n'a pas de bouton (seulement le champ de commande, avec confirmation).

## Développer et déboguer

Tout l'outillage reste dans le projet, sans sudo ni rien dans le système :

| Commande | Effet |
|---|---|
| `labctl/build.sh tools` | installe Go (archive officielle, SHA256 vérifié) et le débogueur delve dans `.tools/` |
| `labctl/build.sh` | compile `bin/labctl` avec le Go du projet |
| `labctl/build.sh docker` | compile dans l'image officielle `golang`, sans Go local |
| `labctl/build.sh test` | `go vet` + tests unitaires |

`source ./activate` met aussi `go`, `gofmt` et `dlv` dans le PATH, avec des caches Go confinés dans `.tools/`.

**VS Code** : ouvrir le dossier `Labs/`, installer l'extension recommandée *Go* (`golang.go`). Elle utilise
automatiquement le Go de `.tools/` (voir `.vscode/settings.json`). Puis F5 :

- *labctl status* / *labctl gui* / *labctl (arguments au choix)* : lancent labctl sous le débogueur ; poser des
  points d'arrêt dans `labctl/internal/...` ;
- *tests du paquet courant* : déboguer un test.

labctl s'exécute sur ton poste, avec les mêmes accès qu'en ligne de commande (libvirt, virsh, clés SSH).

En ligne de commande : `cd labctl && dlv debug ./cmd/labctl -- status`.

## Organisation du code

| Dossier | Rôle |
|---|---|
| `cmd/labctl/` | point d'entrée |
| `internal/lab/` | découverte des modules, lecture de `module.env` / `core.conf` / aide, état libvirt, état détaillé de la passerelle (`core.go`), lecture de la PKI (`pki.go`), complétion |
| `internal/virt/` | accès direct à libvirt (go-libvirt, Go pur, sans virsh) : domaines, IP, snapshots, réseaux ; reconnexion automatique |
| `internal/coreconf/` | génération de la configuration de la passerelle (fonctions pures, testées sans VM) |
| `internal/pki/` | PKI en Go : CA racine, intermédiaire, CA dérivées (nameConstraints), certificats, .p12, déploiement |
| `internal/cli/` | commandes cobra (une par module, `pki`, `status`, `gui`, `completion`) |
| `internal/gui/` | serveur web local et page `static/index.html` (embarquée dans le binaire) |

Convention utilisée par labctl : la VM `<vm>` d'un module `<m>` s'appelle `<m>-<vm>` dans libvirt.
