# Proposition : modules déclaratifs (`module.yaml`)

> Statut : **validée** (décisions en fin de document), à implémenter.

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
- **Tout est facultatif sauf l'essentiel**, avec des valeurs par défaut documentées : un module minimal tient en
  quelques lignes (voir plus bas).

## Le module pentest en YAML

```yaml
# modules/pentest/module.yaml
module: pentest
description: Lab isolé — Kali (attaquant), Metasploitable 2 et une cible web (DVWA + Juice Shop)

network:
  name: pentest-lab               # réseau libvirt isolé, bridge virbr-pentest
  subnet: 10.66.66.0/24           # hôte .1, VMs réservées .2-.99, DHCP libre .100-.199, passerelle core .254
  domain: pentest-lab.omedo.lab   # défaut : <name>.<domaine des labs>

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
| `network.name` / `subnet` / `domain` | name, subnet | réseau isolé du module ; `subnet` en `/24` |
| `pki.ca` | non | crée une CA dérivée limitée au domaine et au sous-réseau du module |
| `dns.aliases` / `hosts` / `links` | non | ce que contient aujourd'hui `core.conf` |

### VM (`vms.<nom>`)

| Clé | Défaut | Description |
|---|---|---|
| `ip` | — : adresse dans la plage DHCP libre (.100-.199), nom DNS par le bail | IP fixe : dernier octet, entre 2 et 99, unique dans le module |
| `mac` | avec `ip` : générée et **ajoutée au YAML** ; sans `ip` : générée à la création, non écrite | MAC de la carte (sert à la réservation DHCP quand `ip` est donnée) |
| `role` | — | `attacker`, `target`, `server`… (affichage, filtres de la GUI) |
| `image` *ou* `source` | — (un des deux) | image cloud du catalogue (cloud-init) *ou* disque fourni |
| `source.url` + `checksum` | — | téléchargement à empreinte figée (`sha256:`, `sha512:`, `md5:`) |
| `source.sums` + `file` | — | empreinte lue dans le fichier publié ; `file` peut contenir `*` (dernière version) |
| `source.extract` | — | fichier à extraire d'une archive (`.7z`, `.zip`, `.tar.*`) ; vmdk/vdi convertis en qcow2 |
| `cpus` / `memory` / `disk` | réglages par défaut | ressources (Mio, Gio) ; `disk` agrandit le disque des images cloud |
| `hardware` | q35, virtio, spice | `machine` (q35/pc), `disk_bus` (virtio/sata/ide), `nic_model` (virtio/e1000), `graphics` (spice/vnc/none), `video`, `osinfo` |
| `guest.agent` | true si `image` | agent invité : clavier depuis l'hôte, IP, commandes |
| `internet` | `off` | `off` : fermé (ouvrable à la main) · `install` : ouvert pendant l'installation · `never` : jamais, même par un lien |
| `packages`, `files`, `run` | — | images cloud seulement (cloud-init) ; `files[].template: true` remplace les `${...}` |
| `certificates` | — | certificats émis par la CA du module et **installés dans la VM** (par SSH) ; `reload` relancé après |
| `snapshot` | — | nom du snapshot pris une fois la VM prête (ex. `clean`) |

### Variables (`${...}`)

Utilisables dans `run`, `certificates[].reload` et les fichiers `template: true` :
`${module.name}`, `${module.domain}`, `${network.subnet}`, `${network.gateway}` (.254), `${vm.name}`, `${vm.ip}`,
`${vm.fqdn}`, `${vms.<autre>.ip}`, `${keyboard}`. Une variable inconnue est une **erreur de validation**.

## Commandes

| Commande | Effet |
|---|---|
| `labctl module list` | modules et leur état |
| `labctl module validate [MODULE]` | contrôle sans rien créer (code de sortie ≠ 0 en cas d'erreur) |
| `labctl module <m> up [VM...]` | crée ce qui manque : réseau, branchement à la passerelle, CA, VMs, certificats, snapshots |
| `labctl module <m> status` | VMs, IP, Internet, écarts entre le YAML et la réalité |
| `labctl module <m> start` / `stop` [VM...] | démarre / arrête |
| `labctl module <m> restore VM [SNAPSHOT]` | revient au snapshot (défaut : celui de `snapshot:`) |
| `labctl module <m> certs` | (ré)émet et réinstalle les certificats déclarés — **règle le problème du certificat web après un renouvellement de la PKI** |
| `labctl module <m> destroy [VM...]` | supprime (confirmation) |

`./lab.sh pentest …` continue de marcher (redirigé vers `labctl module pentest …`) le temps de la transition.
Les VMs existantes (`pentest-kali`, …) sont **reprises telles quelles** : aucune recréation.

## Ce que vérifie le validateur

**Erreurs** (refus d'enregistrer dans la GUI, `validate` en échec) :
- clés inconnues ou mal typées (une faute de frappe ne passe pas en silence), champs obligatoires ;
- noms (module, VM, alias) ; `ip` entre 2 et 99 et unique ; MAC valides et uniques ;
- sous-réseau et domaine qui **chevauchent un autre module** ou un réseau libvirt existant ;
- `image` absente du catalogue ; `source` sans empreinte ; `image` et `source` à la fois ;
- `packages` / `files` / `run` sur une VM sans cloud-init ; fichier `src` introuvable ;
- alias vers une VM inexistante ; lien vers un module inexistant ; certificat dont un nom sort du domaine de la CA ;
- variable `${...}` inconnue ; `internet: never` + une VM qui doit installer des paquets.

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

- Onglet **Modules** : un éditeur YAML par module (coloration, validation à la volée avec les erreurs au bon
  endroit), bouton *Valider*, *Enregistrer* (refusé si invalide), *Appliquer* (`up`).
- Les formulaires actuels (onglet Configuration : alias, réservations, liens) écrivent dans le `module.yaml`
  en **préservant les commentaires** et la mise en forme.
- *Nouveau module* : un squelette commenté à compléter.

## Module minimal

Tout ce qui n'est pas écrit prend sa valeur par défaut :

```yaml
module: demo
network: {name: demo-lab, subnet: 10.70.70.0/24}
vms:
  srv: {}          # image et ressources par défaut, IP dans la plage DHCP libre, srv.demo-lab.omedo.lab
  db: {ip: 20}     # IP fixe .20 : MAC générée et ajoutée au fichier à la création
```

## Décisions

1. **`core.conf` rejoint le `module.yaml`** : une seule source par module. La GUI (onglet Configuration) et les
   commandes `core dhcp|dns|link` écrivent dans le YAML en préservant commentaires et mise en forme ; les
   `core.conf` existants sont importés une fois puis retirés.
2. **Sous-réseaux en `/24`** (convention .1 hôte, .2-.99 IP fixes, .100-.199 DHCP libre, .254 passerelle).
3. **`ip` et `mac` facultatifs.** Sans `ip`, la VM est en DHCP libre (nom DNS par le bail). Avec `ip`, la MAC est
   lue dans le YAML, ou générée et ajoutée au fichier. Tous les champs facultatifs ont une valeur par défaut.
4. **Variables `${module.domain}`** : syntaxe simple, entièrement vérifiée par le validateur.
5. **Stockage** : disques des nouvelles VMs de module dans le dossier des VMs (`~/Virtualisation/kvm/vms/`, sans
   sudo) ; les VMs existantes restent où elles sont.
6. **Changement d'une VM existante** (CPU, mémoire, disque…) : `status` et `validate` signalent l'écart ;
   `labctl module <m> up --apply-changes` l'applique (arrêt et redémarrage de la VM concernée). Jamais de
   redémarrage sans demande explicite.
