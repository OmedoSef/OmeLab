# Module netlab

Un **poste client** (`poste`) pour éprouver les trois fonctions réseau de la passerelle `core` :
**pare-feu par connexion**, **portail captif** et **proxy transparent**. Réseau isolé. Décrit par
[module.yaml](module.yaml), où un seul bloc `connections:` se décommente selon le mécanisme à tester.

Le poste est un **bureau graphique léger** (Debian + XFCE, connexion automatique), avec **Firefox** pour voir
le portail et la navigation, plus `curl`/`dig` pour les tests en ligne de commande. Tout le reste se règle sur
la passerelle.

```bash
labctl netlab up                 # réseau, poste (bureau installé), snapshot « clean » — compter quelques minutes
labctl netlab console poste      # ouvre le bureau (session XFCE de « lab », Firefox) sur la console SPICE
labctl netlab ssh poste          # shell sur le poste ; ou  labctl netlab ssh poste 'commande'
```

Pour chaque test : choisir la forme de la connexion (éditer `module.yaml`, ou l'onglet **Réseau → Connexions**
de la GUI), puis **appliquer** (`labctl core apply` ou le bouton *Appliquer* / la pilule de l'en-tête).

## 1. Pare-feu par connexion (`via: direct`) — forme active par défaut

HTTPS et DNS autorisés, HTTP (port 80) bloqué. `direct` n'ouvre rien tout seul : il faut ouvrir Internet.

```bash
labctl core internet on netlab poste
labctl netlab ssh poste 'curl -s -o /dev/null -w "HTTPS %{http_code}\n" https://example.com'      # 200
labctl netlab ssh poste 'curl -s --max-time 5 http://example.com >/dev/null && echo "HTTP ouvert" || echo "HTTP bloqué"'   # bloqué (port 80 hors de la connexion)
```

Ajoute `tcp/80` aux `ports:` et ré-applique : le HTTP passe à son tour.

## 2. Portail captif (`via: portal`)

Décommente le bloc **portal**, commente le bloc *direct*, applique. **Pas besoin** de `core internet on` :
le portail garde l'accès derrière l'authentification (mot de passe « labs »).

```bash
labctl core apply
# Avant authentification : le HTTP est redirigé vers le portail de la passerelle
labctl netlab ssh poste 'curl -s -o /dev/null -w "%{http_code} -> %{redirect_url}\n" http://example.com'   # 302 -> http://10.73.73.254:8800/
```

Puis ouvre le bureau et **navigue dans Firefox** — c'est le plus parlant pour un portail captif :

```bash
labctl netlab console poste      # dans Firefox : va sur http://example.com → la page du portail s'affiche
                                 # saisis « labs », valide ; ensuite la navigation passe jusqu'à expiration (30 min)
labctl core portal               # sessions en cours ; labctl core portal kick N IP pour en fermer une
```

## 3. Proxy transparent (`via: proxy`)

Décommente un bloc **proxy**, applique, puis ouvre Internet pour le poste (le proxy agit quand Internet est
ouvert pour la source).

```bash
labctl core apply
labctl core internet on netlab poste
labctl netlab ssh poste 'curl -s https://example.com -o /dev/null; curl -s https://www.debian.org -o /dev/null'
labctl core proxy                # journal : source, destination, nom TLS, requêtes HTTP (page Trafic de la GUI aussi)
```

- `mode: log` — observe sans déchiffrer le HTTPS.
- `mode: filter, allow: [...]` — seuls ces domaines passent (les autres : HTTPS coupé, HTTP en 403).
- `mode: intercept` — HTTPS déchiffré. Le poste doit faire confiance à l'autorité d'interception :

  ```bash
  labctl pki trust-vm --interception netlab-poste
  labctl core proxy web            # interface mitmweb (inspecter, modifier, rejouer)
  ```

## Remettre à zéro

```bash
labctl core internet off netlab
labctl netlab restore poste        # revient au snapshot « clean »
```

Isolé, sans donnée sensible : un simple banc d'essai pour les fonctions de la passerelle.
