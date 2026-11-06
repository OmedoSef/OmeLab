# Module pjpt

Petit **Active Directory d'entraînement**, volontairement faible, pour préparer le PJPT. Décrit par
[module.yaml](module.yaml), piloté par `labctl pjpt <commande>`. L'équivalent maison d'un lab AD public comme
GOAD, mais intégré à labctl : une commande construit tout.

Réseau **isolé**, **sans Internet** : l'installation de Windows se fait hors ligne (les pilotes virtio et
OpenSSH sont sur les ISO locales). Les postes d'attaque viennent du module `attacker`, branchés sur le réseau
du lab (`attach:`), comme un attaquant sur le LAN de l'entreprise.

| VM | Libvirt | Rôle | IP | OS |
|---|---|---|---|---|
| `dc01` | `pjpt-dc01` | Contrôleur de domaine, DNS | `.10` | Windows Server 2022 |
| `cli01` | `pjpt-cli01` | Poste joint au domaine | `.20` | Windows 11 Enterprise |
| `cli02` | `pjpt-cli02` | Poste joint au domaine | `.30` | Windows 11 Enterprise |
| `kali` *(attach)* | `attacker-kali` | Attaquant | `.50` | Kali |
| `parrot` *(attach)* | `attacker-parrot` | Attaquant | `.51` | Parrot |

Forêt `pjptlab.local`. Les secrets et réglages (mots de passe Administrateur, DSRM, admin local) sont
regroupés en tête de `module.yaml` sous `vars:` — change-les là, pas dans les scripts.

## Télécharger les ISO Windows

labctl **ne télécharge pas** les ISO Windows (l'Evaluation Center de Microsoft demande un formulaire). À faire
une fois, à la main, puis à déposer dans `~/Virtualisation/kvm/images/sources/` sous le nom attendu par
`module.yaml` :

| VM | Page de téléchargement | Choix | Déposer sous |
|---|---|---|---|
| `dc01` | [Windows Server 2022 Evaluation](https://www.microsoft.com/en-us/evalcenter/download-windows-server-2022) | ISO 64 bits, anglais | `SERVER_WIN_2022_EVAL_x64FRE_en-us.iso` |
| `cli01`, `cli02` | [Windows 11 Enterprise Evaluation](https://www.microsoft.com/en-us/evalcenter/download-windows-11-enterprise) | ISO (édition **Enterprise**, pas LTSC) | `WIN11_EVAL_X64.iso` |

Évaluations valables 180 jours. `module.yaml` fixe aussi l'`edition` (le nom de l'image dans l'ISO) et une
empreinte `sha256` : si Microsoft publie une nouvelle version, l'empreinte ne correspondra plus — remplace-la
dans `module.yaml` par celle du fichier téléchargé (`sha256sum <iso>`), et au besoin l'`edition`.

```bash
labctl attacker up                  # une fois : prépare Kali et Parrot
labctl pjpt up                      # installe dc01 (promotion), puis cli01 et cli02, puis branche les attaquants
labctl pjpt start dc01 cli01        # un seul client suffit pour commencer (16 Go de RAM avec une attaquante)
labctl pjpt restore                 # revient au snapshot « clean » de toutes les VMs (voir ci-dessous)
```

## Comment c'est construit

Les VMs sont provisionnées **une à une, dans l'ordre du fichier** : `dc01` d'abord, entièrement (promotion +
redémarrage + peuplement), puis les clients, qui trouvent donc un domaine déjà prêt à les accueillir. Aucune
dépendance explicite à déclarer. Les scripts PowerShell du dossier [files/](files) sont copiés dans chaque VM
(les `${var.…}` y sont remplacés) et lancés par `run:` :

- [files/dc-setup.ps1](files/dc-setup.ps1) — promeut `dc01` en premier DC de la forêt. Après la promotion, le
  compte `lab` perd l'accès SSH (sur un DC, il n'est plus administrateur) ; une tâche planifiée le replace dans
  *Domain Admins* au redémarrage, pour que labctl se reconnecte et poursuive.
- [files/dc-populate.ps1](files/dc-populate.ps1) — crée les OU, une vingtaine d'utilisateurs, les comptes de
  service et les droits délégués.
- [files/dc-weaknesses.ps1](files/dc-weaknesses.ps1) — réglages système et GPO.
- [files/client-join.ps1](files/client-join.ps1) — pointe le DNS du client sur le DC, joint le domaine, aligne
  l'administrateur local, puis labctl redémarre.

## Ce qui est volontairement faible

Ce sont des **choix de configuration** du lab, pour s'exercer aux techniques du cours PJPT :

- résolution de noms héritée laissée active sur les postes (LLMNR / NBT-NS) ;
- signature SMB non imposée, serveur et clients ;
- un compte sur cinq avec un mot de passe faible et réutilisé ;
- un compte de service `svc_sql` doté d'un SPN et d'un mot de passe faible ;
- un compte de service `svc_backup` sans pré-authentification Kerberos ;
- le même mot de passe administrateur local sur les deux clients ;
- deux comptes (`stannis`, `melisandre`) aux droits trop larges sur le DC et sur *Domain Admins*, pour un
  parcours d'élévation visible dans un graphe type BloodHound ;
- une GPO de démonstration (motif « GPP cpassword »).

Chaque faiblesse est isolée et commentée dans son script : tu peux en retirer pour durcir le lab au fil de ta
progression.

## Snapshots et remise à zéro

Un snapshot `clean` est pris à la fin de l'installation de chaque VM. Pour un AD, **restaure le DC et les
clients ensemble** : si l'un revient en arrière sans les autres, les mots de passe de canal sécurisé se
désynchronisent et les jonctions cassent. `labctl pjpt restore` (sans argument) le fait pour toutes les VMs.

## Limites connues

- Une seule forêt : pas d'abus de relation d'approbation inter-domaines.
- Le motif GPP cpassword se lit dans SYSVOL mais peut ne pas s'appliquer sur un client à jour (MS14-025) :
  utile en lecture, à vérifier avant de conclure à une exploitabilité en engagement réel.
- Licences d'évaluation Windows : 180 jours. Reconstruire après (`labctl pjpt destroy` puis `up`).

## Sécurité

Lab **isolé**, jamais exposé à Internet ni au LAN. Les mots de passe de `module.yaml` sont des valeurs de test
faibles par choix : ne jamais réutiliser cette configuration hors de ce lab.
