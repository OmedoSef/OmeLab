# labctl

Outil de pilotage des labs, en Go : une **CLI avec autocomplétion** et une **GUI web locale**.
Tout est en Go : les modules sont décrits par leur `module.yaml` et construits par le moteur de labctl,
la passerelle core est intégrée, et il n'y a plus de script bash.

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
labctl vm create web1 -n default -n lab-demo:10.10.0.11/24 -l demo -w   # labctl vm create --help
labctl vm ssh web1 ; labctl vm destroy web1
labctl image list ; labctl image get rocky9
labctl net list            # réseaux libvirt ; labctl net create|delete
labctl module validate     # contrôle des module.yaml ; labctl module show pentest : valeurs effectives
labctl pentest status      # module déclaratif : up, status, start, stop, certs…
labctl core status         # passerelle : internet, dhcp, dns, link…
deactivate                 # retire labctl, Go, la complétion et (labs)
```

La complétion couvre tout : commandes, modules,
VMs, snapshots, réservations, alias. Elle vient des module.yaml : un nouveau module ou une nouvelle VM est
complétable tout de suite, sans toucher au Go.

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

Historique : chaque bloc porté en Go est devenu une commande de labctl, testée ; le script bash correspondant
est d'abord devenu un relais, puis a disparu. Migration terminée.

| Bloc | État | Remplace |
|---|---|---|
| PKI | ✅ en Go : `labctl pki` (`internal/pki`, crypto/x509, sans openssl) | `scripts/pki.sh` |
| Génération de la config core (DHCP, DNS, pare-feu, liens) | ✅ en Go : `labctl core config` (`internal/coreconf`) | fonctions `gen_*` du bash |
| Outils VM et réseaux | ✅ en Go : `labctl vm list|ip|snapshot`, `labctl net list|create|delete` (`internal/virt`, libvirt sans virsh) | `vm-list.sh`, `vm-ip.sh`, `vm-snapshot.sh`, `net-*.sh` |
| Images cloud | ✅ en Go : `labctl image list|get` ; empreinte vérifiée (SHA256/512 publiées par les distributions) ; refus de remplacer une image utilisée comme base | `image-get.sh`  |
| Création / suppression de VMs (cloud-init) | ✅ en Go : `labctl vm create|destroy|ssh` ; définition libvirt écrite par labctl (plus de virt-install) ; retour arrière complet si la création échoue | `vm-create.sh`, `vm-destroy.sh`, `vm-ssh.sh`  |
| Édition de la configuration d'un module (réservations, alias, liens) | ✅ en Go, dans le `module.yaml` en préservant commentaires et mise en forme ; utilisée par la GUI et par `core dhcp/dns/link` (via `labctl module edit`) | `core.conf` supprimé |
| Modules déclaratifs | ✅ moteur `labctl <m> up|status|certs…` piloté par `module.yaml` ; éditeur dans la GUI | `modules/pentest/module.sh` supprimé |
| Passerelle core | ✅ intégrée à labctl : `labctl core gw|apply|update|attach|detach|internet|link|dhcp|dns|status…` ; fichiers de la passerelle embarqués (`internal/lab/gwfiles`) | `modules/core/module.sh`, `scripts/lib/module.sh` supprimés |
| Derniers scripts | ✅ `labctl check`, `labctl lab …`, `labctl vm keyboard` (agent invité QEMU par libvirt, sans virsh) ; réglages dans `config.env` à la racine | `scripts/`, `lab.sh` supprimés : plus de bash |

`labctl core config --diff` montre ce qui changerait sur la passerelle (comme le bandeau de la GUI) ;
`labctl core config` sans argument affiche la configuration qui serait appliquée sur la passerelle : pratique pour
contrôler l'effet d'un changement de `module.yaml` avant `labctl core apply`. Au portage, la sortie Go
a été comparée fichier par fichier à celle du bash déployée sur la passerelle : identique.

La PKI Go est compatible avec les fichiers produits par l'ancien script (clés PKCS#8, mêmes chemins) ; un test
vérifie l'interopérabilité avec openssl dans les deux sens.

## GUI

`labctl gui` sert l'interface sur **127.0.0.1:8077** uniquement (`-p` pour un autre port, `--no-browser` pour ne
pas ouvrir le navigateur). `labctl gui --app` l'ouvre dans une **fenêtre d'application** dédiée (Chromium, Chrome,
Edge ou Brave, profil à part dans `~/.cache/labctl/gui-app`) : fermer la fenêtre arrête l'interface.

Un panneau à gauche mène à toutes les pages ; la page courante est dans l'adresse (rechargement, bouton précédent) :

- **Vue d'ensemble** : un module par ligne (réseau, VMs démarrées, Internet, tout démarrer / arrêter, construire)
  et les liens entre modules (`→` un sens, `↔` les deux).
- **Un module** (une entrée par module, avec ses VMs démarrées / déclarées) : en tête, ses actions et l'accès Internet
  du module entier, puis quatre onglets :
  - **VMs** : état, IP, snapshots, Internet VM par VM ; démarrer, arrêter, console (virt-viewer), snapshot,
    restauration ; champ repliable pour lancer n'importe quelle autre commande du module ;
  - **module.yaml** : l'éditeur (coloration, validation à la volée, Ctrl+S, *Appliquer (up)*) ; un `●` signale des
    modifications non enregistrées ;
  - **Fichiers** : contenu de `modules/<m>/files/` (copié dans les VMs par `files:`) : pour chaque fichier, les VMs
    et destinations qui l'utilisent (⟨t⟩ = template), fichiers référencés mais absents ; envoi (bouton ou
    glisser-déposer, 512 Mio au plus par fichier, remplacement après confirmation), nouveau fichier ou dossier,
    édition des fichiers texte (1 Mio au plus), téléchargement, renommage, suppression. Rien ne sort de `files/` :
    ni `..`, ni chemin absolu, ni lien symbolique suivi. Les fichiers sont copiés à la création d'une VM : une VM
    existante ne reçoit pas les changements d'elle-même ;
  - **Réseau** : réservations DHCP (nom, IP, MAC), alias DNS, liens avec les autres modules **dans les deux sens**
    (ce module peut joindre… / peut être joint par…, une case par sens), et **connexions restreintes**
    (`connections:` : vers un module, une VM ou Internet, depuis certaines VMs, sur certains ports ; voir
    [docs/module-yaml.md](../docs/module-yaml.md)) avec celles que d'autres modules déclarent vers celui-ci ;
    une connexion peut passer par un **portail captif** (authentification par clic, mot de passe ou conditions,
    durée de session, détection par les OS, annonce DHCP 114), ou par le **proxy** de la passerelle (mitmproxy :
    `log`, `filter` par domaine, `intercept` avec l'autorité d'interception). Les sessions de portail sont sur la page
    Passerelle (bouton *Déconnecter*).
- **Trafic (proxy)** : journal du proxy, filtrable par module, action et texte ; bouton *Ouvrir mitmweb*
  (inspecter, modifier, rejouer, par un tunnel SSH).
- **+ Nouveau module…** : nom demandé, puis l'éditeur sur un squelette (brouillon jusqu'au premier enregistrement).
- **Passerelle** : créer, démarrer, arrêter, *Appliquer la configuration*, *Mettre à jour*, console ; état des services
  (`gw`, `gwdns@dhcp`, DNS ouvert/fermé de chaque module), baux DHCP, sources autorisées et bloquées dans le pare-feu.
- **PKI** : CA racine, intermédiaire et CA dérivées avec leurs limites (domaine, sous-réseau) et leur expiration ;
  certificats émis avec leurs noms ; création d'une CA dérivée, émission d'un certificat, déploiement sur une
  VM, installation de la CA racine (ou de l'autorité d'interception du proxy) dans une VM ; téléchargement des **certificats publics** (jamais des clés
  privées ni des `.p12`, qui restent dans `pki/`) ; **maintenance de la chaîne** (renouveler l'intermédiaire, une
  CA dérivée, ou toute la chaîne : voir ci-dessous). `pki trust-host` (sudo) reste en ligne de commande.

Les modifications de la section `dns` (réservations, alias, liens) sont enregistrées tout de suite dans le
`module.yaml` mais **appliquées seulement à la demande** : un bandeau, en haut de toutes les pages, signale les
changements en attente, montre exactement ce qui changera sur la passerelle (diff), et *Appliquer* les déploie.
Mêmes validations que la ligne de commande (IP ou MAC déjà prise, nom de VM, cible d'alias inconnue…).

L'action en cours s'affiche sur la ligne de la VM (ou sur le module) concernée, puis une notification donne le
résultat (en cas d'échec, avec la dernière ligne d'erreur) ; la sortie complète est dans le journal en bas de page.

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
| `internal/module/` | modules déclaratifs : lecture stricte du `module.yaml`, valeurs effectives, variables, validateur, édition préservant la mise en forme |
| `internal/lab/` | découverte des modules, lecture des `module.yaml` (et anciens `module.env`) / aide, état libvirt, état détaillé de la passerelle (`core.go`), lecture de la PKI (`pki.go`), complétion |
| `internal/virt/` | accès direct à libvirt (go-libvirt, Go pur, sans virsh) : domaines, IP, snapshots, réseaux ; reconnexion automatique |
| `internal/coreconf/` | génération de la configuration de la passerelle (fonctions pures, testées sans VM) |
| `internal/pki/` | PKI en Go : CA racine, intermédiaire, CA dérivées (nameConstraints), certificats, .p12, déploiement |
| `internal/cli/` | commandes cobra (une par module, `pki`, `status`, `gui`, `completion`) |
| `internal/gui/` | serveur web local et page `static/index.html` (embarquée dans le binaire) |
| `internal/portal/` | service de portail captif, lancé sur la passerelle (`labctl gw-portal serve`) |

Convention utilisée par labctl : la VM `<vm>` d'un module `<m>` s'appelle `<m>-<vm>` dans libvirt.
