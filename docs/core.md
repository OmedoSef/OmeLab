# Passerelle core (intégrée à labctl)

Services communs à tous les modules, centralisés sur une petite VM **`core-gw`** (Debian 13, 1 Gio) :

- **DHCP** de tous les modules, avec IP fixes par adresse MAC ;
- **DNS** : noms des VMs, alias, noms Internet seulement pour les VMs autorisées ;
- **passerelle Internet**, fermée par défaut, qu'on ouvre pour un module entier ou pour certaines VMs ;
- **liens entre modules** : quels modules peuvent se joindre et se résoudre.

```
            Internet
               │
     NAT libvirt « default » (192.168.122.0/24)
               │ eth0 (WAN : SSH depuis l'hôte uniquement)
          ┌────┴─────┐
          │ core-gw  │  nftables (gwctl) · dnsmasq : 1 DHCP + 2 DNS par module
          └┬────────┬┘
       .254│        │.254
   pentest-lab   <autre module>      bridges isolés, sans DHCP ni DNS libvirt
```

> Sans `core-gw` démarrée, les modules n'ont **ni adresse IP, ni DNS, ni Internet**. Démarrer la passerelle
> avant les VMs : `labctl core start`.

## Commandes

```bash
labctl core gw                       # crée la passerelle et la branche sur chaque module existant
labctl core status                   # cartes, services, état Internet, liens

labctl core internet on netlab               # ouvre tout le module (refusé pour un module vulnérable : pentest, pjpt)
labctl core internet on attacker kali        # ou seulement ces VMs
labctl core internet off netlab [VM...]      # referme
labctl core internet status

labctl core link pentest autre               # pentest peut joindre et résoudre « autre »
labctl core link pentest autre --both        # dans les deux sens
labctl core unlink pentest autre [--both]
labctl core links
# accès restreints (ports, VMs précises, Internet) : section connections: du module.yaml (docs/module-yaml.md)

labctl core dhcp list [MODULE...]            # réservations (IP fixes)
labctl core dhcp leases [MODULE]             # baux en cours
labctl core dhcp add pentest dc01 40 52:54:00:aa:bb:cc   # 10.66.66.40 + dc01.<domaine>
labctl core dhcp del pentest dc01
labctl core dhcp release 52:54:00:aa:bb:cc   # libère un bail (automatique à la suppression d'une VM)

labctl core dns list [MODULE]
labctl core dns alias add pentest dvwa web   # dvwa.pentest.omelab.internal -> web
labctl core dns alias del pentest dvwa

labctl core apply                    # régénère tout (réseau, DHCP, DNS, pare-feu) sur la passerelle
labctl core update                   # pousse une nouvelle version des fichiers système, puis apply
labctl core attach|detach MODULE     # (fait automatiquement par « <module> up » / « destroy »)
labctl core start|stop|ssh|destroy
```

## Où est la configuration

Tout ce qui concerne un module côté core vit **dans son `module.yaml`**, versionné :

| Fichier | Contenu | Modifié par |
|---|---|---|
| `module.yaml` | `network` (sous-réseau, domaine), `vms` (IP, MAC, `internet: never`), et section `dns` : `hosts` (réservations en plus), `aliases`, `links` | à la main, ou `core dhcp`, `core dns`, `core link`, la GUI (commentaires et mise en forme préservés) |

L'état **Internet ouvert / fermé** n'est pas de la configuration : il vit dans la passerelle et se remet à zéro
quand elle redémarre.

## Garanties

Vérifiées par des tests depuis des VMs de deux modules :

| Situation | Résultat |
|---|---|
| Module fermé (défaut) | Pas d'Internet. Noms externes : **REFUSED immédiat**. Noms du lab : résolus |
| Ouvert pour certaines VMs | Seules ces VMs sortent et résolvent les noms externes |
| Module ouvert | Internet OK pour ses VMs, sauf les VMs `internet: never` (msf2) : `internet on pentest msf2` est refusé |
| Toujours | LAN, `192.168.122.1` et tous les réseaux privés injoignables ; rien n'entre depuis le WAN sauf SSH |
| Sans lien (défaut) | Un module ne joint ni ne résout aucun autre module |
| `link A B` | A joint et résout B ; B ne joint pas A (les réponses reviennent, c'est tout) |
| `link A B --both` | Dans les deux sens |
| VM `internet: never` | N'initie rien hors de son module, même vers un module lié (elle reste joignable) |
| Lien ouvert | N'ouvre pas Internet |
| Redémarrage de `core-gw` | Internet refermé partout ; DHCP, DNS et liens rechargés depuis la configuration |
| Suppression d'une VM (`labctl vm destroy`, `<module> destroy`) | Son bail est libéré : IP disponible, nom DNS retiré tout de suite |
| `core apply` | Les baux des réseaux qui ne sont plus branchés sont purgés |
| Module `vulnerable: true` | `core internet on` refusé (module et VMs) ; ses connexions et liens sortants ne sont jamais générés, même ajoutés à la main ; Internet seulement le temps de l'installation d'une VM `internet: install` (tests automatisés) |
| Module à l'identifiant douteux | Un nom de module, de VM ou d'alias, une MAC, un domaine ou une cible de connexion qui pourrait casser la configuration (saut de ligne, accolade, guillemet, `/`…) : le module est **écarté** de la passerelle avec un message (`core apply`, `core config`), même s'il n'a jamais été validé ni lancé. Les autres modules restent servis (tests automatisés) |

Les refus sont explicites (ICMP *administratively prohibited*) : l'échec est immédiat et non un délai d'attente.

> 💡 Les VMs gardent un cache DNS : juste après un changement d'accès, une VM peut encore résoudre (ou non)
> un nom vu juste avant. Ça ne change rien à ce qui passe réellement. Vider le cache : `resolvectl flush-caches`.

## DNS

- Le domaine d'un module vient de son `module.yaml` : `network.domain` (complet), ou `network.subdomain`
  (`<sous-domaine>.<racine>`), sinon `<réseau>.<racine>` ; la racine est `LABS_PKI_DOMAIN` (`omelab.internal`
  par défaut, choisie à `labctl init`). Il peut avoir plusieurs niveaux (`k8s.client-a.omelab.internal`), et doit
  rester sous la racine ou sous un suffixe privé (`.internal`, `.local`…) : jamais un domaine d'Internet. En changer après coup impose de recréer la CA dérivée du module.
- Le DHCP fournit le domaine de recherche : `kali`, `dvwa`… marchent aussi en nom court.
- Les VMs en DHCP libre (plage `.100`–`.199`) sont aussi résolues, sous le nom qu'elles annoncent.
- Les domaines des modules non liés et tout le domaine racine du lab répondent NXDOMAIN, sans jamais être relayés vers Internet.

## Fonctionnement interne

| Fichier (embarqué dans labctl : `labctl/internal/lab/gwfiles/`) | Installé sur la passerelle | Rôle |
|---|---|---|
| `gwctl` | `/usr/local/sbin/gwctl` | génère le pare-feu nftables (accès Internet, VMs bloquées, liens, redirections DNS) ; libère et purge les baux DHCP |
| `gwdhcp-hook` | `/usr/local/sbin/gwdhcp-hook` | à chaque bail DHCP, met à jour les noms dynamiques du module |
| `gw.service` | `/etc/systemd/system/` | applique le pare-feu au démarrage (Internet fermé) |
| `gwdns@.service` | `/etc/systemd/system/` | instances dnsmasq : `dhcp`, et `<module>-open` / `<module>-closed` par module |
| `90-gw.conf` | `/etc/sysctl.d/` | routage IPv4 activé, IPv6 désactivé |
| `gwportal.service` | `/etc/systemd/system/` | portails captifs (`connections: … via: portal`) : `labctl gw-portal serve` (copie de labctl sur la passerelle) |
| `gwproxy.service` | `/etc/systemd/system/` | proxy transparent (`via: proxy`) : mitmproxy avec `gwproxy.py` |
| `gwproxy.py` | `/usr/local/lib/gwproxy/` | extension mitmproxy : journal, filtrage par domaine (`mode: filter`) |

Ces fichiers sont embarqués dans l'exécutable. Pour en mettre un au point sans recompiler, déposer sa version
dans `core/files/<nom>` à la racine de `Labs/` : elle remplace celle de labctl. Puis `labctl core update`
l'installe sur la passerelle.

`labctl core apply` génère sur l'hôte la configuration, depuis le `module.yaml` de chaque module branché et le
câblage et les connexions locaux de ce poste (`network.local.yaml`, à la racine), puis
l'envoie sur la passerelle (`labctl core config` l'affiche sans rien appliquer, `--diff` : seulement ce qui change) :

- l'adresse `.254` de la passerelle sur chaque réseau (`/etc/systemd/network/50-gw-<module>.network`) ;
- le DHCP (`/etc/gwdns/dhcp.conf`) ;
- les deux instances DNS de chaque module (`/etc/gwdns/<module>-{open,closed}.conf`) ;
- les VMs bloquées (`/etc/gwdns/blocked`) ;
- les règles des liens et des connexions restreintes (`/etc/gwdns/forward-modules.nft`), et les restrictions de
  ports d'un accès Internet ouvert (`/etc/gwdns/forward-internet.nft`) : générées par labctl, insérées par `gwctl`
  dans la chaîne `forward` (`/etc/gwdns/links` reste écrit pour information) ;
- les portails captifs (`connections: … via: portal`) : ensembles des sources authentifiées
  (`/etc/gwdns/sets.nft`, entrées à expiration), redirections du HTTP non authentifié (`/etc/gwdns/prerouting.nft`)
  et configuration du service `gwportal` (`/etc/gwdns/portal.json`). Ce service est **labctl lui-même**
  (`labctl gw-portal serve`), copié sur la passerelle par `labctl core gw` et `labctl core update` ; il écoute sur
  les ports 8800 et suivants, côté modules seulement, et garde ses sessions dans `/run/gw/` pour les remettre en
  place après chaque `apply`. `labctl core portal` : sessions en cours ; `labctl core portal kick N IP` ;
- le proxy (`connections: … via: proxy`) : redirections du HTTP(S) vers `:8080` (`/etc/gwdns/prerouting.nft`) et
  règles lues par le module mitmproxy (`/etc/gwdns/proxy.json`). Le service `gwproxy` (mitmweb 12.3.1 en mode
  transparent, `/opt/mitmproxy`, module `/usr/local/lib/gwproxy/gwproxy.py`) n'est installé et lancé que si une
  connexion l'utilise : l'archive officielle est téléchargée sur l'hôte, vérifiée par son empreinte SHA-256, puis
  copiée dans la passerelle. Interface mitmweb sur `127.0.0.1:8081` de la passerelle seulement (tunnel SSH :
  `labctl core proxy web`), journal dans `/var/log/gwproxy/flows.jsonl` (`labctl core proxy`). Le port 8080
  n'accepte que le trafic redirigé par le pare-feu.

La passerelle a **1 Gio** de mémoire (mitmweb garde les échanges en mémoire, 2000 au plus) : `labctl core update`
porte une passerelle plus ancienne (512 Mio) à 1 Gio, avec un redémarrage d'une trentaine de secondes.

Chaque requête DNS d'une VM est redirigée par nftables vers l'instance `open` de son module si la VM a le droit
de sortir, sinon vers l'instance `closed`, qui ne connaît que les noms du lab.
