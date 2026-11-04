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
labctl gui                 # ouvre l'interface dans le navigateur
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

## GUI

`labctl gui` sert l'interface sur **127.0.0.1:8077** uniquement (`-p` pour un autre port, `--no-browser` pour ne
pas ouvrir le navigateur). On y voit et pilote :

- la passerelle (démarrer / arrêter) ;
- par module : état des VMs, IP, snapshots, accès Internet (module entier ou VM par VM) ;
- par VM : démarrer, arrêter, console (virt-viewer), snapshot, restauration ;
- la matrice des liens entre modules (dès qu'il y a deux modules) ;
- n'importe quelle commande d'un module (champ « Commande du module »).

La sortie des commandes s'affiche en direct dans le journal en bas de page.

**Sécurité** : seul ton poste peut s'y connecter (127.0.0.1). Chaque lancement tire un jeton aléatoire, placé
dans l'adresse ouverte (`#t=...`) et exigé pour chaque action, ce qui empêche un autre site web ouvert dans le
navigateur de piloter tes labs. L'en-tête `Host` est vérifié contre le *DNS rebinding*. Une seule action tourne
à la fois, et seules les commandes des modules existants sont acceptées. La suppression d'un module entier
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
| `internal/lab/` | découverte des modules, lecture de `module.env` / `core.conf` / aide, état libvirt, complétion |
| `internal/cli/` | commandes cobra (une par module, `status`, `gui`, `completion`) |
| `internal/gui/` | serveur web local et page `static/index.html` (embarquée dans le binaire) |

Convention utilisée par labctl : la VM `<vm>` d'un module `<m>` s'appelle `<m>-<vm>` dans libvirt.
