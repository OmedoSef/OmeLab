# Passerelle core (intégrée à labctl)

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
> avant les VMs : `labctl core start`.

## Commandes

```bash
labctl core gw                       # crée la passerelle et la branche sur chaque module existant
labctl core status                   # cartes, services, état Internet, liens

labctl core internet on pentest              # ouvre tout le module
labctl core internet on pentest kali web     # ou seulement ces VMs
labctl core internet off pentest [VM...]     # referme
labctl core internet status

labctl core link pentest autre               # pentest peut joindre et résoudre « autre »
labctl core link pentest autre --both        # dans les deux sens
labctl core unlink pentest autre [--both]
labctl core links

labctl core dhcp list [MODULE]               # réservations (IP fixes)
labctl core dhcp leases [MODULE]             # baux en cours
labctl core dhcp add pentest dc01 40 52:54:00:aa:bb:cc   # 10.66.66.40 + dc01.<domaine>
labctl core dhcp del pentest dc01
labctl core dhcp release 52:54:00:aa:bb:cc   # libère un bail (automatique à la suppression d'une VM)

labctl core dns list [MODULE]
labctl core dns alias add pentest dvwa web   # dvwa.pentest-lab.omedo.lab -> web
labctl core dns alias del pentest dvwa

labctl core apply                    # régénère tout (réseau, DHCP, DNS, pare-feu) sur la passerelle
labctl core update                   # pousse une nouvelle version des fichiers système, puis apply
labctl core attach|detach MODULE     # (fait automatiquement par « <module> network » / « destroy »)
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

- Le domaine d'un module est `network.domain` dans son `module.yaml` (défaut `<réseau>.omedo.lab`). Il peut avoir
  plusieurs niveaux (`k8s.client-a.omedo.lab`). En changer après coup impose de recréer la CA dérivée du module.
- Le DHCP fournit le domaine de recherche : `kali`, `dvwa`… marchent aussi en nom court.
- Les VMs en DHCP libre (plage `.100`–`.199`) sont aussi résolues, sous le nom qu'elles annoncent.
- Les domaines des modules non liés et tout `omedo.lab` répondent NXDOMAIN, sans jamais être relayés vers Internet.

## Fonctionnement interne

| Fichier (embarqué dans labctl : `labctl/internal/lab/gwfiles/`) | Installé sur la passerelle | Rôle |
|---|---|---|
| `gwctl` | `/usr/local/sbin/gwctl` | génère le pare-feu nftables (accès Internet, VMs bloquées, liens, redirections DNS) ; libère et purge les baux DHCP |
| `gwdhcp-hook` | `/usr/local/sbin/gwdhcp-hook` | à chaque bail DHCP, met à jour les noms dynamiques du module |
| `gw.service` | `/etc/systemd/system/` | applique le pare-feu au démarrage (Internet fermé) |
| `gwdns@.service` | `/etc/systemd/system/` | instances dnsmasq : `dhcp`, et `<module>-open` / `<module>-closed` par module |
| `90-gw.conf` | `/etc/sysctl.d/` | routage IPv4 activé, IPv6 désactivé |

Ces fichiers sont embarqués dans l'exécutable. Pour en mettre un au point sans recompiler, déposer sa version
dans `core/files/<nom>` à la racine de `Labs/` : elle remplace celle de labctl. Puis `labctl core update`
l'installe sur la passerelle.

`labctl core apply` génère sur l'hôte la configuration, depuis le `module.yaml` de chaque module branché, puis
l'envoie sur la passerelle (`labctl core config` l'affiche sans rien appliquer, `--diff` : seulement ce qui change) :

- l'adresse `.254` de la passerelle sur chaque réseau (`/etc/systemd/network/50-gw-<module>.network`) ;
- le DHCP (`/etc/gwdns/dhcp.conf`) ;
- les deux instances DNS de chaque module (`/etc/gwdns/<module>-{open,closed}.conf`) ;
- les liens et les VMs bloquées (`/etc/gwdns/links`, `/etc/gwdns/blocked`).

Chaque requête DNS d'une VM est redirigée par nftables vers l'instance `open` de son module si la VM a le droit
de sortir, sinon vers l'instance `closed`, qui ne connaît que les noms du lab.
