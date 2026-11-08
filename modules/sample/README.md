# Module sample

Exemple **complet et fonctionnel**, à lire pour comprendre le système et à copier pour démarrer un module.
Un petit serveur web intranet (`nginx`), servi en **HTTPS** avec un certificat de la CA du module, joignable
par son nom. Réseau isolé comme tout module. Décrit par [module.yaml](module.yaml).

Pour un squelette vide à remplir soi-même, préférer *+ Nouveau module…* dans la GUI (squelette commenté, valide tel quel). Ce module-ci est l'inverse :
tout est rempli et marche, pour servir de référence vivante.

## Ce qu'il montre

- un **réseau isolé** nommé, avec sous-réseau et domaine fixés ;
- une **CA de module** (`pki.ca`) et un **certificat** émis pour le serveur (`certificates:`), rechargé après
  installation (`reload:`) ;
- une **image du catalogue** (`debian13`), des **paquets** (`packages:`) et de l'**Internet à l'installation
  seulement** (`internet: install`) ;
- des **fichiers** copiés dans la VM, dont des **modèles** (`template: true`) où les `${...}` sont remplacés ;
- des **variables de module** (`vars:`) réutilisées dans la page (`${var.title}`, `${var.owner}`) ;
- un **alias DNS** (`www`) ;
- en commentaire : une 2ᵉ VM, des accès restreints (`connections:`) et le branchement d'un attaquant
  (`attach:`).

## Essayer

```bash
labctl sample up                 # crée le réseau, la CA, la VM, le certificat, puis le snapshot « clean »
labctl sample status             # IP, état, snapshots
```

Depuis une VM du même lab (ou en ajoutant la CA du module à ton poste), la page répond sur
`https://www.sample.<racine>/` (`<racine>` = domaine racine du lab, `omelab.internal` par défaut) avec un certificat valide. En HTTP, redirection vers HTTPS.

```bash
labctl sample restore            # revient au snapshot « clean »
labctl sample destroy            # supprime la VM et le réseau
```

## Fichiers

- [files/index.html](files/index.html) — page d'accueil (modèle : titre et propriétaire viennent de `vars:`) ;
- [files/sample.conf](files/sample.conf) — configuration nginx HTTPS (modèle : noms et IP injectés).

Isolé, sans donnée sensible : un simple exemple à adapter.
