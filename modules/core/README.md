# Module core

Services communs à tous les modules, centralisés sur une petite VM **`core-gw`** (Debian 13, 512 Mo) :

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
> avant les VMs : `./lab.sh core start`.

## Commandes

```bash
./lab.sh core gw                       # crée la passerelle et la branche sur chaque module existant
./lab.sh core status                   # cartes, services, état Internet, liens

./lab.sh core internet on pentest              # ouvre tout le module
./lab.sh core internet on pentest kali web     # ou seulement ces VMs
./lab.sh core internet off pentest [VM...]     # referme
./lab.sh core internet status

./lab.sh core link pentest autre               # pentest peut joindre et résoudre « autre »
./lab.sh core link pentest autre --both        # dans les deux sens
./lab.sh core unlink pentest autre [--both]
./lab.sh core links

./lab.sh core dhcp list [MODULE]               # réservations (IP fixes)
./lab.sh core dhcp leases [MODULE]             # baux en cours
./lab.sh core dhcp add pentest dc01 40 52:54:00:aa:bb:cc   # 10.66.66.40 + dc01.<domaine>
./lab.sh core dhcp del pentest dc01
./lab.sh core dhcp release 52:54:00:aa:bb:cc   # libère un bail (automatique à la suppression d'une VM)

./lab.sh core dns list [MODULE]
./lab.sh core dns alias add pentest dvwa web   # dvwa.pentest-lab.omedo.lab -> web
./lab.sh core dns alias del pentest dvwa

./lab.sh core apply                    # régénère tout (réseau, DHCP, DNS, pare-feu) sur la passerelle
./lab.sh core update                   # pousse une nouvelle version des fichiers système, puis apply
./lab.sh core attach|detach MODULE     # (fait automatiquement par « <module> network » / « destroy »)
./lab.sh core start|stop|ssh|destroy
```

## Où est la configuration

Tout ce qui concerne un module côté core vit **dans le dossier du module**, versionné :

| Fichier | Contenu | Modifié par |
|---|---|---|
| `module.env` | `SUBNET`, `DOMAIN`, VMs du module (`HOSTS` = dernier octet, `MACS`), `NO_INTERNET` | à la main |
| `core.conf` | `host NOM OCTET MAC` (réservations en plus), `alias NOM CIBLE`, `link MODULE` | `core dhcp`, `core dns`, `core link` |

L'état **Internet ouvert / fermé** n'est pas de la configuration : il vit dans la passerelle et se remet à zéro
quand elle redémarre.

## Garanties

Vérifiées par des tests depuis des VMs de deux modules :

| Situation | Résultat |
|---|---|
| Module fermé (défaut) | Pas d'Internet. Noms externes : **REFUSED immédiat**. Noms du lab : résolus |
| Ouvert pour certaines VMs | Seules ces VMs sortent et résolvent les noms externes |
| Module ouvert | Internet OK pour ses VMs, sauf `NO_INTERNET` (msf2) : `internet on pentest msf2` est refusé |
| Toujours | LAN, `192.168.122.1` et tous les réseaux privés injoignables ; rien n'entre depuis le WAN sauf SSH |
| Sans lien (défaut) | Un module ne joint ni ne résout aucun autre module |
| `link A B` | A joint et résout B ; B ne joint pas A (les réponses reviennent, c'est tout) |
| `link A B --both` | Dans les deux sens |
| VM `NO_INTERNET` | N'initie rien hors de son module, même vers un module lié (elle reste joignable) |
| Lien ouvert | N'ouvre pas Internet |
| Redémarrage de `core-gw` | Internet refermé partout ; DHCP, DNS et liens rechargés depuis la configuration |
| Suppression d'une VM (`vm-destroy.sh`, `<module> destroy`) | Son bail est libéré : IP disponible, nom DNS retiré tout de suite |
| `core apply` | Les baux des réseaux qui ne sont plus branchés sont purgés |

Les refus sont explicites (ICMP *administratively prohibited*) : l'échec est immédiat et non un délai d'attente.

> 💡 Les VMs gardent un cache DNS : juste après un changement d'accès, une VM peut encore résoudre (ou non)
> un nom vu juste avant. Ça ne change rien à ce qui passe réellement. Vider le cache : `resolvectl flush-caches`.

## DNS

- Le domaine d'un module est `DOMAIN=` dans son `module.env` (défaut `<réseau>.omedo.lab`). Il peut avoir
  plusieurs niveaux (`k8s.client-a.omedo.lab`). En changer après coup impose de recréer la CA dérivée du module.
- Le DHCP fournit le domaine de recherche : `kali`, `dvwa`… marchent aussi en nom court.
- Les VMs en DHCP libre (plage `.100`–`.199`) sont aussi résolues, sous le nom qu'elles annoncent.
- Les domaines des modules non liés et tout `omedo.lab` répondent NXDOMAIN, sans jamais être relayés vers Internet.

## Fonctionnement interne

| Fichier (dans `files/`) | Installé sur la passerelle | Rôle |
|---|---|---|
| `gwctl` | `/usr/local/sbin/gwctl` | génère le pare-feu nftables (accès Internet, VMs bloquées, liens, redirections DNS) ; libère et purge les baux DHCP |
| `gwdhcp-hook` | `/usr/local/sbin/gwdhcp-hook` | à chaque bail DHCP, met à jour les noms dynamiques du module |
| `gw.service` | `/etc/systemd/system/` | applique le pare-feu au démarrage (Internet fermé) |
| `gwdns@.service` | `/etc/systemd/system/` | instances dnsmasq : `dhcp`, et `<module>-open` / `<module>-closed` par module |
| `90-gw.conf` | `/etc/sysctl.d/` | routage IPv4 activé, IPv6 désactivé |

`./lab.sh core apply` génère sur l'hôte avec `labctl core-config` (Go), depuis `module.env` et `core.conf` de chaque
module branché, puis envoie le tout sur la passerelle (`labctl core-config` seul affiche ce qui serait appliqué) :

- l'adresse `.254` de la passerelle sur chaque réseau (`/etc/systemd/network/50-gw-<module>.network`) ;
- le DHCP (`/etc/gwdns/dhcp.conf`) ;
- les deux instances DNS de chaque module (`/etc/gwdns/<module>-{open,closed}.conf`) ;
- les liens et les VMs bloquées (`/etc/gwdns/links`, `/etc/gwdns/blocked`).

Chaque requête DNS d'une VM est redirigée par nftables vers l'instance `open` de son module si la VM a le droit
de sortir, sinon vers l'instance `closed`, qui ne connaît que les noms du lab.
