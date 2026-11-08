# Modules déclaratifs (`module.yaml`)

Référence du format des modules (les décisions de conception sont en fin de document).

### Dans l'éditeur (VS Code…)

Chaque `module.yaml` commence par une ligne qui désigne son **schéma** à l'éditeur :

```yaml
# yaml-language-server: $schema=../../module.schema.json
```

Avec l'extension YAML de Red Hat dans VS Code (ou tout éditeur basé sur yaml-language-server : Neovim, Helix…),
on obtient la **complétion des clés**, leur **description au survol** et les **fautes de frappe soulignées**
(`subdomian:`, un nombre attendu…). Le schéma est généré depuis le code de labctl : il suit toujours sa version.

- `labctl init` pose `module.schema.json` à la racine du lab ; créer un module depuis la GUI (« + Nouveau module… ») le pose aussi s'il manque ;
- après une mise à jour de labctl : `labctl module schema --write` (sans `--write` : affiché) ;
- il est aussi publié avec chaque release, et installé par le `.deb` dans `/usr/share/labctl/`.

Sans cette ligne, VS Code applique à tort le schéma d'un autre outil (cfgd) dont les fichiers s'appellent aussi
`module.yaml`, d'où de fausses erreurs (« Property module is not allowed »).

## Principes

- Un module = un dossier `modules/<nom>/` avec un **`module.yaml`** et, si besoin, des fichiers à copier dans
  les VMs (`files/`). **Aucun code spécifique à un module** : labctl est un moteur générique qui lit le YAML.
  Modifier un module = modifier le YAML, **sans recompiler**.
- Le YAML décrit **l'état voulu**. `labctl module <nom> up` crée ce qui manque (réseau, CA, certificats, VMs) et
  ne touche pas à ce qui existe : on peut le relancer sans risque.
- **`labctl module validate`** contrôle un module (ou tous) **sans rien créer** : structure, cohérence, fichiers
  référencés, conflits avec les autres modules. La GUI l'utilise à chaque modification et refuse d'enregistrer
  une configuration invalide.
- L'**isolation n'est pas optionnelle** : une VM de module n'a de cartes que sur le réseau de son module (règle
  imposée par le moteur et le validateur, pas par chaque module).
- Le module `core` reste intégré à labctl (c'est l'infrastructure commune), mais son contenu actuel par module
  (`core.conf` : alias, réservations, liens) **rejoint le `module.yaml`**.
- **Tout est facultatif sauf le nom du module**, avec des valeurs par défaut à plusieurs niveaux (voir
  *Valeurs par défaut*) : un module minimal tient en quelques lignes.

## Le module pentest en YAML

> Exemple d'origine : Kali est depuis passée dans le module `attacker`, branchée sur `pentest` par `attach:`
> (voir [modules/pentest/module.yaml](../modules/pentest/module.yaml) et [modules/attacker](../modules/attacker/module.yaml)).

```yaml
# modules/pentest/module.yaml
module: pentest
description: Lab isolé — Metasploitable 2 et une cible web (DVWA + Juice Shop)

network:
  name: pentest-lab               # réseau libvirt isolé, bridge virbr-pentest
  subnet: 10.66.66.0/24           # hôte .1, VMs réservées .2-.99, DHCP libre .100-.199, passerelle core .254
  subdomain: pentest              # domaine = pentest.<domaine racine du lab> ; défaut : <name>.<racine>

pki:
  ca: pentest                     # CA dérivée du module, limitée à network.domain et network.subnet

dns:
  aliases:                        # <alias>.<domaine> -> VM du module (ou nom complet)
    dvwa: web
    juice: web
  hosts: []                       # réservations DHCP/DNS en plus : - {name: dc01, ip: 40, mac: 52:54:00:...}
  links: []                       # modules que celui-ci peut joindre et résoudre

vms:
  kali:
    role: attacker
    ip: 10                        # dernier octet, réservé par DHCP
    mac: 52:54:00:66:66:10
    cpus: 4
    memory: 4096                  # Mio
    source:                       # disque fourni par l'éditeur (pas une image cloud)
      sums: https://cdimage.kali.org/current/SHA256SUMS
      file: kali-linux-*-qemu-amd64.7z      # cherché dans le fichier d'empreintes (dernière version)
      extract: "*.qcow2"                    # fichier à prendre dans l'archive
    hardware: {osinfo: debiantesting, graphics: spice}
    guest: {agent: true}          # agent invité : clavier réglé depuis l'hôte (pas de cloud-init)
    internet: off                 # off (défaut) | install | never
    snapshot: clean               # snapshot pris une fois la VM prête

  msf2:
    role: target
    ip: 20
    mac: 52:54:00:66:66:20
    cpus: 1
    memory: 1024
    source:
      url: https://sourceforge.net/projects/metasploitable/files/Metasploitable2/metasploitable-linux-2.0.0.zip/download
      checksum: md5:abb0a95bd4422397ed235a7284e2ed7f   # empreinte figée (seule publiée : MD5)
      extract: "*.vmdk"                                 # converti en qcow2 automatiquement
    hardware: {machine: pc, disk_bus: ide, nic_model: e1000, graphics: vnc, video: vga, osinfo: ubuntu8.04}
    internet: never               # n'initie jamais rien hors du module, même passerelle ouverte ou lien
    snapshot: clean

  web:
    role: target
    ip: 30
    mac: 52:54:00:66:66:30
    image: ubuntu2404             # image cloud du catalogue : cloud-init (utilisateur, clé, clavier, CA)
    cpus: 2
    memory: 3072
    disk: 20                      # Gio
    internet: install             # Internet ouvert pour elle seule le temps de l'installation, puis refermé
    packages: [docker.io, docker-compose-v2]
    files:
      - {src: files/compose.yaml, dest: /opt/targets/compose.yaml}
      - {src: files/nginx.conf, dest: /opt/targets/nginx.conf, template: true}   # ${...} remplacés
    certificates:
      - name: web                 # émis par la CA du module ; SAN : web.<domaine>, les alias ci-dessous, l'IP
        dns: [dvwa, juice]
        ip: true
        cert: /opt/targets/certs/web.crt     # chaîne complète
        key: /opt/targets/certs/web.key
        reload: docker compose -f /opt/targets/compose.yaml restart tls
    run:
      - docker compose -f /opt/targets/compose.yaml up -d --quiet-pull
    snapshot: clean
```

Et à côté, `modules/pentest/files/compose.yaml` et `files/nginx.conf` (aujourd'hui générés en dur dans le bash),
par exemple :

```nginx
# files/nginx.conf — template : ${...} remplacé à la copie
server {
  listen 443 ssl; server_name web.${module.domain} dvwa.${module.domain} ${vm.ip};
  ssl_certificate /certs/web.crt; ssl_certificate_key /certs/web.key;
  location / { proxy_pass http://dvwa:80; proxy_set_header Host $host; }
}
```

Le `module.sh` du pentest (≈ 500 lignes de bash) disparaît : tout est dans ces trois fichiers.

## Référence

### Module

| Clé | Obligatoire | Description |
|---|---|---|
| `module` | oui | nom (= nom du dossier) |
| `description` | non | affichée par `labctl`, la GUI et la complétion |
| `vulnerable` | non | `true` : module de **cibles volontairement vulnérables** (voir *Modules vulnérables*) |
| `network` | non | sans cette section : réseau `<module>-lab`, domaine `<module>-lab.<domaine des labs>`, sous-réseau `/24` libre choisi par labctl et **écrit dans le YAML** au premier `up` (stable ensuite) |
| `network.name` / `subnet` | non | pour les fixer soi-même ; `subnet` en `/24` |
| `network.domain` / `subdomain` | non | domaine du module. `subdomain: X` → `X.<domaine racine du lab>` (se coller sous la racine) ; `domain: X.local` → domaine **complet**, pris tel quel (ignore la racine) ; défaut `<réseau>.<racine>`. Les deux ensemble : `domain` l'emporte. `domain` doit être sous la racine du lab ou sous un suffixe privé (`.internal`, `.local`, `.home.arpa`, `.test`, `.lan`, `.lab`, `.corp`…) : **jamais un domaine d'Internet** (la PKI du lab émettrait des certificats pour lui, et le DNS du lab le détournerait) |
| `defaults` | non | valeurs communes aux VMs du module (`image`, `cpus`, `memory`, `disk`, `hardware`, `internet`, `snapshot`…) |
| `pki.ca` | non | crée une CA dérivée limitée au domaine et au sous-réseau du module |
| `dns.aliases` / `hosts` | non | alias DNS, réservations DHCP de machines hors `vms:` |
| `dns.links` | non | modules que celui-ci peut joindre et résoudre : **tout**, tous ports, du module entier vers le module entier |
| `connections` | non | accès **restreints** (voir ci-dessous) |
| `attach` | non | VMs d'autres modules branchées sur le réseau de celui-ci (voir *VMs branchées*) |
| `vars` | non | valeurs nommées du module, reprises partout via `${var.<nom>}` (voir *Variables*) : mots de passe de test, nom de domaine… à écrire une fois |

### Modules vulnérables (`vulnerable: true`)

Un module qui contient des cibles volontairement vulnérables (pentest, pjpt…) se déclare comme tel, en tête :

```yaml
module: pjpt
vulnerable: true
```

C'est une **zone rouge** : rien n'en sort, et labctl le garantit à chaque étape, pas seulement à la validation.

- **Jamais d'Internet ouvert à la main** : `labctl core internet on` est refusé pour le module comme pour ses VMs
  (la GUI n'affiche pas de bouton d'ouverture). Seule exception : une VM `internet: install` a Internet **le temps
  de son installation** (ex. images Docker de pentest/web), refermé dès qu'elle est prête — y compris si labctl est
  interrompu (Ctrl+C, arrêt) ; et si labctl a été tué net, chaque application de la configuration de la passerelle
  retire toute ouverture restée sur un module vulnérable.
- **Aucune connexion ni lien sortant** : `connections:` et `dns.links` sont refusés à la validation, et ignorés
  par la passerelle même si le fichier est modifié à la main.
- **Ses VMs ne se branchent pas ailleurs** (`attach:` d'un autre module, câblage local).
- Les autres modules peuvent, eux, **le joindre** : postes d'attaque branchés sur son réseau, liens ou connexions
  déclarés vers lui (les réponses reviennent, rien d'autre).
- `labctl check` signale un pare-feu de l'hôte inactif (`labctl hostfw`), et le `up` d'un module vulnérable
  l'affiche en avertissement : sans lui, les cibles peuvent joindre les services de ton poste par l'adresse `.1`
  de leur réseau.

### VMs branchées (`attach:` et câblage local)

Un module cible accueille une VM d'un autre module, typiquement un poste d'attaque du module `attacker`, qui
reçoit alors une carte de plus **sur le réseau des cibles**, comme un attaquant branché sur le LAN.

**Recommandé : le câblage local.** Quelle VM tu branches sur quel module est une décision *propre à ta machine*,
pas une propriété du lab. Elle vit donc dans `network.local.yaml` à la racine (git-ignoré, **non distribué**),
section `attach:`, et se pilote à la souris dans la GUI (onglet VMs d'un module → *Brancher une VM…* /
*Débrancher*). Un module reste ainsi autonome et distribuable, sans dépendre d'un module d'attaque précis :

```yaml
# network.local.yaml (racine du lab) — câblage et connexions propres à ce poste ; « on » = module cible
attach:
  - {on: pentest, vm: attacker/kali, ip: 10}
  - {on: pjpt,    vm: attacker/kali, ip: 50}
```

En ligne de commande comme à la souris : `labctl pentest attach attacker/kali` (adresse libre choisie) et
`labctl pentest detach attacker/kali`. La complétion propose les VMs branchables, puis celles à débrancher.

**Possible aussi : `attach:` dans le `module.yaml`** du module cible, pour un bundle volontairement autonome. Même
forme, sans `on` (le module cible est celui du fichier). Une cible ou une VM absente est alors **tolérée** (simple
avertissement au `up`), pour qu'un lab distribué fonctionne même sans le module d'attaque attendu.

```yaml
attach:
  - {vm: attacker/kali, ip: 10}      # attacker-kali reçoit une carte sur ce réseau, IP .10 réservée
  - {vm: attacker/parrot, ip: 11}
```

| Clé | Obligatoire | Description |
|---|---|---|
| `vm` | oui | `<module>/<vm>` : une VM à IP fixe d'un autre module |
| `ip` | oui | dernier octet réservé sur le réseau de ce module (2-99) |
| `mac` | non | générée et écrite au premier `up` |

- `labctl <module> up` branche les cartes (à chaud si la VM tourne) ; `destroy` du module les retire. La VM se
  crée et se gère dans son propre module (ex. `labctl attacker up kali`).
- Sur ce réseau, la carte reçoit son IP par DHCP **sans route par défaut ni DNS** : Internet et le DNS restent ceux
  de la carte principale (module d'origine), dont le DNS résout alors aussi les noms de ce module
  (`<vm>.<domaine de ce module>`, ex. `kali.pentest.<racine>`).
- La VM est sur le même réseau local que les cibles : attaques de réseau local (LLMNR/NBT-NS, relais SMB, mitm6…)
  possibles, sans règle de pare-feu à écrire. Elle ne route pas d'un réseau à l'autre (transfert IP désactivé,
  défaut des distributions) : les modules restent isolés entre eux et les cibles n'atteignent pas Internet.
- Le nom (`kali`) ne doit pas déjà être une VM ou une réservation du module.

### Connexions (`connections:`)

Une connexion autorise une partie du module à joindre une cible, sur certains ports seulement :

```yaml
connections:
  - {to: pentest/web, from: [srv], ports: [tcp/80, tcp/443]}   # srv -> la VM web de pentest, HTTP(S) seulement
  - {to: pentest, ports: [icmp]}                               # tout le module -> tout pentest, ping seulement
  - {to: internet, from: [kali], ports: [tcp/443, udp/53]}     # quand Internet est ouvert à kali : HTTPS et DNS
```

| Clé | Obligatoire | Description |
|---|---|---|
| `to` | oui | un module, `module/vm` (VM à IP fixe ou réservation), ou `internet` |
| `from` | non | VMs à IP fixe ou réservations de ce module ; défaut : tout le module |
| `ports` | non | `tcp/80`, `udp/53`, plages `tcp/8000-8100`, `icmp` ; défaut : tous |
| `via` | non | `direct` (défaut), `portal` (portail captif) ou `proxy` (proxy de la passerelle) : voir ci-dessous |
| `portal` / `proxy` | non | options (avec `via: portal` / `via: proxy`) |

- **Vers un module** : seul ce qui est déclaré passe, dans ce sens-là ; les réponses reviennent, le sens inverse
  reste fermé (il se déclare dans l'autre module). Les noms de la cible deviennent résolvables, pour le module
  entier (une instance DNS par module) : c'est le pare-feu qui filtre par VM et par port.
- **Vers `internet`** : **ouvre** ses `ports` pour ses sources, qu'Internet soit ouvert à la main ou non (comme une
  connexion entre modules) ; le reste est refusé pour ces sources, et elles obtiennent la résolution des noms publics.
  `labctl core internet on` n'est plus un prérequis : c'est un raccourci « accès complet » pour les VMs **sans**
  connexion `internet` (une VM qui a une connexion garde son périmètre, même quand le module est ouvert à tout).
- Une cible ne peut pas être à la fois dans `dns.links` et dans `connections` (ambigu : retirer l'un des deux).
- Inchangé : les VMs `internet: never` ne sortent jamais (ni vers Internet, ni vers les modules liés), le LAN reste
  injoignable, rien n'entre depuis le WAN.
- Comme le reste de la section réseau : enregistré tout de suite, appliqué sur la passerelle avec
  `labctl core apply` (ou le bandeau *Appliquer* de la GUI).

#### Connexions locales (`network.local.yaml`)

Comme le câblage, certaines connexions sont une décision **propre à ta machine**, pas une propriété du module :
une bascule de test qu'on allume/éteint (portail, proxy `log`/`intercept`), ou l'accès d'une VM branchée
localement. Elles vivent alors dans le **même** `network.local.yaml` à la racine du lab (git-ignoré, **non
distribué**), section `connections:`, et le module reste autonome. Même forme qu'une connexion, avec `on:` =
module cible :

```yaml
# network.local.yaml (racine du lab) — fusionné aux connections: du module à son chargement
connections:
  - {on: netlab, to: internet, from: [poste], via: proxy, proxy: {mode: log}}
```

Dans la GUI (onglet *Réseau* → *Connexions*), la case **« local à ce poste »** du formulaire écrit là plutôt que
dans le `module.yaml` ; ces connexions portent le badge *local* et se retirent au même endroit. Tout le reste
(pare-feu, proxy, apply) les traite exactement comme les `connections:` du module.

### Portail captif (`via: portal`)

Pour observer comment des machines se comportent derrière un portail captif, vers Internet ou vers un autre module :

```yaml
connections:
  - to: internet
    from: [srv]
    via: portal
    portal: {auth: password, password: lab, duration: 30m, title: Wi-Fi Hôtel}
  - {to: pentest/web, ports: [tcp/80], via: portal, portal: {auth: terms, detect: false}}
```

| Clé de `portal` | Défaut | Description |
|---|---|---|
| `auth` | `click` | `click` (un bouton), `password`, `terms` (case « j'accepte ») |
| `password` | | avec `auth: password` ; mot de passe de lab, en clair dans le fichier |
| `duration` | `1h` | durée d'une session (`1m` à `24h`) ; ensuite la source est de nouveau captée |
| `title` | Portail du lab | titre de la page |
| `detect` | `true` | les tests de connectivité des OS (Android `generate_204`, Apple `hotspot-detect`, Windows `connecttest`, Firefox, NetworkManager) sont redirigés : le système affiche « se connecter au réseau ». `false` : ils reçoivent la réponse « connecté » (portail discret), seule la navigation est captée |
| `api` | `false` | annonce RFC 8910 (option DHCP 114, pour tout le module) et API RFC 8908 (`/api`) |
| `https` | `reject` | HTTPS non authentifié : refus immédiat, ou `drop` (sans réponse, délai d'attente) |

- **Avant authentification** : le HTTP (`tcp/80`) vers la cible est redirigé vers le portail de la passerelle
  (`http://<passerelle .254>:<8800+n>/`), le reste est refusé ; vers Internet, le DNS externe est résolu (sans quoi
  les tests de connectivité n'atteindraient pas le portail ; c'est aussi la porte des tunnels DNS, comme sur un vrai
  portail).
- **Après** : la connexion est ouverte (ses `ports`) jusqu'à la fin de la session ; nftables retire la source
  seul à l'expiration (et coupe ses connexions en cours). Comme toute connexion, le portail n'a pas besoin de
  l'ouverture manuelle (`core internet on`) ; sa particularité est de garder l'accès **derrière l'authentification**.
- Les sessions survivent à `labctl core apply` ; `labctl core portal` les liste, `labctl core portal kick N IP` en
  ferme une (aussi dans la GUI, page Passerelle).
- Le HTTPS ne peut pas être capté sans alerte de certificat : comme sur un vrai portail, seul le HTTP est redirigé.
- Une VM `internet: never` ne passe pas par un portail vers Internet (refusé par le validateur, et bloquée par la
  passerelle de toute façon).

### Proxy (`via: proxy`)

Le HTTP et le HTTPS de la connexion passent, de façon transparente (rien à régler dans les VMs), par le proxy de
la passerelle : **mitmproxy 12.3.1**, installé dans la passerelle à la première connexion qui en a besoin.

```yaml
connections:
  - {to: internet, from: [kali], via: proxy, proxy: {mode: intercept}}
  - {to: internet, via: proxy, proxy: {mode: filter, allow: ["*.debian.org", github.com]}}
  - {to: pentest/web, ports: [tcp/80, tcp/22], via: proxy, proxy: {mode: log}}
```

| Clé de `proxy` | Défaut | Description |
|---|---|---|
| `mode` | `log` | `log` : journal (source, destination, nom TLS, requêtes HTTP), HTTPS non déchiffré ; `filter` : idem, et filtrage par domaine ; `intercept` : HTTPS déchiffré, requêtes et réponses complètes |
| `allow` / `deny` | | avec `mode: filter` : seuls ces domaines passent (`allow`), ou tous sauf ceux-ci (`deny`) ; `*.debian.org` ne couvre pas `debian.org` lui-même |
| `ports` | | ports HTTP(S) en plus de `tcp/80` et `tcp/443` (ex. `tcp/8080`) |

- Seuls 80, 443 et `proxy.ports` passent par le proxy ; les autres ports suivent `ports:` de la connexion, en direct.
- ⚠️ **`mode: filter` n'est pas une frontière de sécurité.** Il décide d'après le nom que le client annonce (SNI en
  HTTPS, en-tête `Host` en HTTP), pas d'après l'adresse réellement jointe : un programme qui annonce un nom autorisé
  peut joindre n'importe quelle adresse, et en mode `deny` une connexion sans nom annoncé passe. C'est la limite de
  tout filtrage par nom de domaine. Il sert à **observer** le comportement d'une machine face à un filtrage (mise à
  jour, télémétrie…), pas à contenir une machine hostile : pour cela, pas de connexion vers Internet du tout
  (et `vulnerable: true` pour un module de cibles).
- Vers Internet, le proxy **ouvre** le HTTP(S) de ses sources (Internet ouvert à la main ou non) en le faisant passer par mitmproxy.
- Refus du filtre : HTTPS coupé, HTTP avec une page 403 du proxy.
- **Autorité d'interception** : racine à part (« OmeLab Interception CA », `pki/interception/`, hors de la chaîne
  OmeLab), créée à la première utilisation, dont la clé n'est copiée que dans la passerelle. Elle n'est installée
  que dans les VMs choisies : `labctl pki trust-vm --interception VM` (ou la page PKI de la GUI). Une VM qui ne lui
  fait pas confiance voit un certificat inconnu, ce qui permet d'observer son comportement.
- Journal : `labctl core proxy [-n N]`, ou la page **Trafic** de la GUI (filtres par module, action, texte) ;
  interface de mitmproxy (inspecter, modifier, rejouer) : `labctl core proxy web` ou le bouton *Ouvrir mitmweb*
  (tunnel SSH vers `127.0.0.1:8081` de la passerelle, mot de passe affiché).

### VM (`vms.<nom>`)

| Clé | Défaut | Description |
|---|---|---|
| `ip` | — : adresse dans la plage DHCP libre (.100-.199), nom DNS par le bail | IP fixe : dernier octet, entre 2 et 99, unique dans le module |
| `mac` | avec `ip` : générée et **ajoutée au YAML** ; sans `ip` : générée à la création, non écrite | MAC de la carte (sert à la réservation DHCP quand `ip` est donnée) |
| `role` | — | `attacker`, `target`, `server`… (affichage, filtres de la GUI) |
| `image`, `source` *ou* `windows` | — (un des trois) | image cloud du catalogue (cloud-init), disque fourni, *ou* installation de Windows depuis un ISO (voir *Windows*) |
| `source.url` + `checksum` | — | téléchargement à empreinte figée (`sha256:`, `sha512:`, `md5:`) |
| `source.sums` + `file` | — | empreinte lue dans le fichier publié ; `file` peut contenir `*` (dernière version) |
| `source.extract` | — | fichier à extraire d'une archive (`.7z`, `.zip`, `.tar.*`) ; vmdk/vdi convertis en qcow2. Un nom ou un motif, sans chemin ni tiret en tête ; une archive qui contient un lien symbolique (ou un disque dont un morceau en est un) est refusée |
| `cpus` / `memory` / `disk` | 1 vCPU, 2048 Mio, 20 Gio (voir *Valeurs par défaut*) | ressources (Mio, Gio) ; `disk` agrandit le disque des images cloud |
| `image` (si ni `image` ni `source`) | `debian13` | image cloud par défaut |
| `hardware` | q35, virtio, spice, bios | `machine` (q35/pc), `disk_bus` (virtio/sata/ide), `nic_model` (virtio/e1000), `graphics` (spice/vnc/none), `video`, `osinfo`, `firmware` (bios/efi), `secure_boot`, `tpm` (TPM 2.0 émulé, swtpm). Une VM `efi` (OVMF) prend ses snapshots **arrêtée** (limite de libvirt) : labctl l'arrête, la photographie et la redémarre (restauration : coupure, retour, redémarrage) |
| `guest.agent` | true si `image` | agent invité : clavier depuis l'hôte, IP, commandes |
| `internet` | `off` | `off` : fermé (ouvrable à la main) · `install` : ouvert pendant l'installation · `never` : jamais, même par un lien |
| `keyboard` | clavier de l'hôte | disposition appliquée à la VM (console et X) à la création ; par défaut celle de l'hôte (`LABS_KEYBOARD`, sinon `localectl`) ; à préciser (ex. `us`) pour forcer une autre disposition |
| `packages`, `files`, `run` | — | images cloud (cloud-init) ; Windows : `files` et `run` (PowerShell, par SSH) ; `files[].template: true` remplace les `${...}`. `files[].src` est **relatif au dossier du module** (ex. `files/x.conf`) : chemin absolu, `~`, `..` et lien symbolique sont refusés — un module ne peut pas copier dans une VM un fichier de l'hôte (clé de `pki/`, `~/.ssh`…) |
| `certificates` | — | certificats émis par la CA du module et **installés dans la VM** (par SSH) ; `reload` relancé après |
| `snapshot` | — | nom du snapshot pris une fois la VM prête (ex. `clean`) |

### Windows (`vms.<nom>.windows`)

Installation **sans intervention** depuis l'ISO de Microsoft (Evaluation Center : formulaire d'inscription, donc
jamais téléchargé par labctl ; le déposer dans `~/Virtualisation/kvm/images/sources/`) :

```yaml
vms:
  dc01:
    ip: 10
    cpus: 2
    memory: 4096
    disk: 60
    windows:
      iso: ~/Virtualisation/kvm/images/sources/SERVER_WIN_2022_EVAL_x64FRE_en-us.iso
      checksum: sha256:3e4fa6d8507b554856fc9ca6079cc402df11a8b79344871669f0251535255325
      edition: Windows Server 2022 SERVERSTANDARD      # nom de l'image dans install.wim
      password: Labs-P4ssw0rd!                          # Administrateur et compte lab
    files:
      - {src: files/setup.ps1, dest: C:/labs/setup.ps1}
    run:
      - Install-WindowsFeature AD-Domain-Services -IncludeManagementTools
      - reboot                                        # redémarre la VM et attend son retour
    snapshot: clean
```

| Clé | Défaut | Description |
|---|---|---|
| `iso` | — | chemin de l'ISO (`~` accepté) ; seule une image ISO 9660 est acceptée, jamais un autre fichier de l'hôte |
| `checksum` | — (avertissement) | `sha256:…` : vérifiée une fois par ISO (mémorisée à côté, `<iso>.verified`) |
| `edition` | — | image à installer (ex. `Windows Server 2022 SERVERSTANDARD` = avec bureau, `Windows 11 Enterprise Evaluation`) |
| `drivers` | déduit de l'édition | dossier des pilotes virtio-win : `2k22`, `2k19`, `w11`… |
| `password` | — | mot de passe de l'Administrateur et du compte `lab` (en clair : mot de passe de lab) |
| `timezone` | `Romance Standard Time` | fuseau horaire Windows |

Déroulé (`labctl <module> up`) :

1. ISO vérifié ; **virtio-win** (pilotes, outils invités, agent QEMU ; Fedora, version figée) et **OpenSSH**
   (Win32-OpenSSH de Microsoft, version figée) téléchargés en cache, empreintes vérifiées.
2. labctl génère `autounattend.xml` (partitions UEFI, édition, langue anglaise et **clavier de ton poste**, nom de
   machine, comptes, bureau à distance) et un script de fin, sur un petit ISO de réponses, puis démarre la VM sur
   les trois ISO (UEFI par défaut ; Windows 11 : `hardware: {tpm: true, secure_boot: true}`).
3. Setup s'installe seul (15 à 40 min) ; première session (compte `lab`) : outils invités virtio, agent QEMU,
   **OpenSSH Server** (ta clé SSH pour les administrateurs, PowerShell par défaut), réseau privé, ping, RDP ; arrêt.
4. labctl retire les ISO (l'ISO de réponses, qui contient le mot de passe, est supprimé), redémarre la VM, attend
   SSH (`lab@<ip>`), copie `files` (chemins `C:/…`), lance `run` en PowerShell, puis prend le snapshot.

`labctl <module> ssh <vm>` ouvre une session PowerShell ; la console graphique est dans virt-manager/virt-viewer.

### Variables (`${...}`)

Les `${...}` sont résolus dans **tous les champs** du module.yaml, **sauf** `module:`, `description:` et le bloc
`vars:` lui-même. Deux familles :

- **globales** — `${var.<nom>}` (valeurs de `vars:`), `${module.name}`, `${module.domain}`, `${network.subnet}`,
  `${network.gateway}` (.254), `${keyboard}` — valables **partout** (iso, image, source, connexions, alias DNS…) ;
- **par VM** — `${vm.name}`, `${vm.ip}`, `${vm.fqdn}` — seulement dans les champs **d'une VM** (le contexte est
  cette VM). Au niveau module (dans `network:`, `connections:`, `dns:`…) il n'y a pas de « VM courante » : on y
  désigne une VM par son nom avec `${vms.<nom>.ip}` / `${vms.<nom>.fqdn}`.

Une variable inconnue, un `${var.…}` absent de `vars:`, un `${vm.…}` hors d'une VM, ou un cycle entre variables
de `vars:` sont des **erreurs de validation**. Les champs numériques/structurels (`ip`, `cpus`…) ne sont pas
concernés. Les fichiers `files: template: true` sont aussi templatés (leur **contenu**, pas seulement le chemin).

Idéal pour écrire une valeur une seule fois et la reprendre partout — mot de passe de test, nom de domaine,
chemin d'ISO partagé par plusieurs VMs :

```yaml
vars:
  domain: pjptlab.local
  admin_password: "Labs-P4ssw0rd!"
  iso_win11: ~/Virtualisation/kvm/images/sources/WIN11_EVAL_X64.iso   # écrit une fois
vms:
  dc01:
    windows: {iso: "${var.iso_win11}", password: "${var.admin_password}"}
  client01:
    windows: {iso: "${var.iso_win11}", password: "${var.admin_password}"}   # repris tel quel
```

#### Variables de niveau lab (`lab.yaml`)

Un fichier `lab.yaml` à la **racine du lab** peut définir un bloc `vars:` partagé par **tous les modules** du
dossier. Pour un nom donné, la valeur du lab **l'emporte** sur le défaut que le module écrit dans son propre
`vars:`. Un module écrit donc un défaut raisonnable (il reste utilisable seul), et l'utilisateur ajuste en **un
seul endroit** ce qui change d'une install à l'autre — chemins d'ISO, mot de passe, domaine… — sans toucher aux
modules :

```yaml
# lab.yaml (racine du lab)
vars:
  iso_win11: ~/mes-isos/WIN11_EVAL_X64.iso   # surcharge le défaut de chaque module qui l'utilise
```

Précédence d'un `${var.X}` : `lab.local.yaml` (non versionné, pour les secrets et les réglages propres au
poste) puis `lab.yaml`, puis le `vars:` du module. Un lab sans `lab.yaml` ne change rien : chaque module garde
ses propres valeurs. Dans `lab.yaml`, une variable peut en référencer une autre (`${var.autre}`) ou `${keyboard}`.

## Commandes

| Commande | Effet |
|---|---|
| `labctl module list` | modules et leur état |
| `labctl module validate [MODULE]` | contrôle sans rien créer (code de sortie ≠ 0 en cas d'erreur) |
| `labctl <m> check` | prérequis de l'hôte pour ce module (KVM, libvirt, outils, clé SSH, PKI, passerelle, disque) |
| `labctl <m> up [VM...] [--apply-changes] [--dry-run]` | crée ce qui manque : réseau, branchement à la passerelle, CA, VMs, certificats, snapshots ; `--dry-run` montre le plan sans rien créer |
| `labctl <m> status` | VMs, IP, snapshots, écarts entre le YAML et la réalité |
| `labctl <m> start` / `stop [--force]` [VM...] | démarre / arrête (isolation vérifiée avant tout démarrage) |
| `labctl <m> snapshot VM [NOM]` / `restore VM [SNAPSHOT]` | snapshot ; retour au snapshot (défaut : celui de `snapshot:`) |
| `labctl <m> certs [--renew]` | (ré)émet et réinstalle les certificats déclarés : réémis d'office s'ils viennent d'une autre CA (PKI renouvelée), si leurs noms ont changé ou s'ils expirent dans 30 jours |
| `labctl <m> keyboard VM [DISPOSITION]` | clavier d'une VM (SSH, sinon agent invité), puis redémarrage |
| `labctl <m> ssh VM [cmd]` | shell SSH (images cloud) |
| `labctl <m> attach MODULE/VM [IP]` | branche une VM d'un autre module sur ce réseau (câblage local ; adresse libre si omise) |
| `labctl <m> detach MODULE/VM` | débranche une VM d'un autre module (retire la carte et le câblage local) |
| `labctl <m> destroy [VM...]` | supprime (confirmation) ; sans VM : tout le module, sauf le `module.yaml`, la CA et les sources téléchargées |

Les mêmes commandes existent sous `labctl module <m> …`.
Les VMs existantes (`pentest-kali`, …) sont **reprises telles quelles** : aucune recréation. Leurs disques
restent où ils sont (pool `pentest-lab`) ; les nouvelles VMs vont dans le dossier des VMs.

Ce que fait `up`, dans l'ordre :
1. sous-réseau : s'il manque, un `/24` libre (`10.N.N.0`) est choisi et écrit dans le fichier ;
2. réseau libvirt isolé `<réseau>` (pont `virbr-<module>`, hôte en `.1`), puis branchement de la passerelle ;
3. CA dérivée du module (`pki.ca`), limitée au domaine et au sous-réseau ;
4. pour chaque VM absente : MAC écrite dans le fichier si `ip` est fixe, réservation DHCP appliquée sur la
   passerelle, disque (image cloud ou `source` téléchargée, vérifiée, extraite, convertie), Internet ouvert
   le temps de l'installation si `internet: install`, attente de cloud-init (ou de l'agent invité puis
   réglage du clavier, ou d'une IP), Internet refermé, snapshot de référence ;
5. pour chaque VM existante : isolation vérifiée, écarts signalés (ou appliqués avec `--apply-changes`),
   certificats installés s'ils diffèrent ;
6. configuration DHCP/DNS de la passerelle appliquée si elle a changé.

## Ce que vérifie le validateur

**Erreurs** (refus d'enregistrer dans la GUI, `validate` en échec) :
- clés inconnues ou mal typées (une faute de frappe ne passe pas en silence), champs obligatoires ;
- noms (module, VM, alias) ; `ip` entre 2 et 99 et unique ; MAC valides et uniques ;
- sous-réseau et domaine qui **chevauchent un autre module** ou un réseau libvirt existant ;
- `image` absente du catalogue ; `source` sans empreinte ; `image` et `source` à la fois ;
- `packages` / `files` / `run` sur une VM sans cloud-init ; fichier `src` introuvable ou hors du dossier du module ;
- alias vers une VM inexistante ; lien vers un module inexistant ; certificat dont un nom sort du domaine de la CA ;
- connexion : cible inconnue (module, VM sans IP fixe), source qui n'est ni une VM à IP fixe ni une réservation,
  port mal formé, cible déjà dans `dns.links`, `via` inconnu ou pas encore disponible ; portail : `auth`
  inconnu, `password` manquant, durée hors de 1m-24h, `https` inconnu, source `internet: never` vers Internet ;
  proxy : `mode` inconnu, `allow`/`deny` hors de `mode: filter` ou les deux à la fois, domaine ou port mal formé ;
- variable `${...}` inconnue ; `internet: never` + une VM qui doit installer des paquets ;
- `internet: never` sans `ip` (la passerelle bloque par adresse) ;
- nom de module de plus de 9 caractères (pont `virbr-<module>`) ou réservé (`list`, `validate`, `core`…).

**Avertissements** (n'empêchent pas) :
- empreinte MD5 seulement ; `file` avec `*` (version non figée) ;
- ressources totales des VMs supérieures à la RAM de l'hôte ;
- cible (`role: target`) avec `internet` autre que `never`/`off` ;
- VM sans `snapshot` ; écarts entre le YAML et une VM déjà créée (ex. mémoire changée : voir *Décisions*).

Exemple de sortie :

```
$ labctl module validate pentest
modules/pentest/module.yaml
  erreur  vms.web.ip : 20 déjà utilisé par vms.msf2
  erreur  dns.aliases.intranet : la VM « intra » n'existe pas
  avert.  vms.msf2.source.checksum : MD5 seulement (seule empreinte publiée)
2 erreurs, 1 avertissement
```

## GUI

- Onglet **module.yaml** de la page d'un module : éditeur avec coloration,
  numéros de ligne, **validation à la volée** (erreurs et avertissements à leur ligne, cliquables).
  - *Enregistrer* (Ctrl+S) : refusé tant qu'il reste une erreur, ou si le fichier a été modifié ailleurs
    depuis son ouverture (*Recharger le fichier* pour repartir de la version sur le disque).
  - *Appliquer (up)* : construit le module enregistré ; le fichier est relu ensuite (sous-réseau, MAC ajoutés).
  - *Nouveau module…* : squelette commenté, valide tel quel ; *Créer le module* crée `modules/<nom>/` et `files/`.
- Onglet **Réseau** de la page d'un module : alias, réservations, liens (une case par sens) et connexions
  restreintes, écrits dans le `module.yaml` en **préservant les commentaires** et la mise en forme ; une
  connexion refusée par le validateur n'est pas écrite.

## Valeurs par défaut

Chaque champ facultatif d'une VM est pris, dans l'ordre, du premier niveau qui le définit :

| Priorité | Niveau | Exemple |
|---|---|---|
| 1 | la VM | `vms.kali.cpus: 4` |
| 2 | `defaults:` du module | `defaults: {memory: 2048, image: ubuntu2404}` |
| 3 | tes réglages (`config.local.env`, sinon `config.env`, à la racine) | `LABS_DEFAULT_CPUS=1`, `LABS_DEFAULT_MEMORY`, `LABS_DEFAULT_DISK`, `LABS_DEFAULT_IMAGE` |
| 4 | intégré à labctl | 1 vCPU, 2048 Mio, 20 Gio, `debian13`, q35/virtio/spice, `internet: off`, pas de snapshot |

`labctl module <m> status` (et la GUI) affiche la valeur effective de chaque champ et son origine.

## Module minimal

Tout ce qui n'est pas écrit prend sa valeur par défaut :

```yaml
module: demo
vms:
  srv: {}          # tout par défaut : debian13, 1 vCPU, 2 Gio, DHCP libre, srv.demo-lab.omelab.internal
  db: {ip: 20}     # IP fixe .20 : MAC générée et ajoutée au fichier à la création
```

Au premier `up`, labctl complète le fichier avec ce qu'il a choisi, pour que tout reste stable ensuite :

```yaml
module: demo
network:                     # ajouté au premier up : sous-réseau libre choisi par labctl
  name: demo-lab
  subnet: 10.70.70.0/24
vms:
  srv: {}
  db: {ip: 20, mac: 52:54:00:3a:91:c4}   # MAC ajoutée
```

## Décisions

1. **`core.conf` rejoint le `module.yaml`** : une seule source par module. La GUI (onglet Réseau) et les
   commandes `core dhcp|dns|link` écrivent dans le YAML en préservant commentaires et mise en forme ; les
   `core.conf` existants sont importés une fois puis retirés.
2. **Sous-réseaux en `/24`** (convention .1 hôte, .2-.99 IP fixes, .100-.199 DHCP libre, .254 passerelle).
3. **Tout est facultatif sauf le nom du module.** `ip` et `mac` : sans `ip`, la VM est en DHCP libre (nom DNS
   par le bail) ; avec `ip`, la MAC est lue dans le YAML, ou générée et ajoutée au fichier. `network` : sans
   section, sous-réseau `/24` libre choisi puis écrit dans le YAML. Ressources : défauts à plusieurs niveaux
   (VM, `defaults:` du module, réglages, intégré), **1 vCPU** par défaut.
4. **Variables `${module.domain}`** : syntaxe simple, entièrement vérifiée par le validateur.
5. **Stockage** : disques des nouvelles VMs de module dans le dossier des VMs (`~/Virtualisation/kvm/vms/`, sans
   sudo) ; les VMs existantes restent où elles sont.
6. **Changement d'une VM existante** (CPU, mémoire, disque…) : `status` et `validate` signalent l'écart ;
   `labctl module <m> up --apply-changes` l'applique (arrêt et redémarrage de la VM concernée). Jamais de
   redémarrage sans demande explicite.
