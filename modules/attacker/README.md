# Module attacker

Postes d'attaque : Kali et Parrot Security. Décrit par [module.yaml](module.yaml), piloté par `labctl attacker <commande>`.

Chaque poste a sa carte principale sur le réseau `attacker-lab` (Internet à la demande, comme tout module), et une
carte de plus sur le réseau de chaque lab cible qui l'accueille (`attach:` dans le `module.yaml` de la cible) :
il est alors sur le même réseau local que les cibles, comme un attaquant branché sur le LAN.

| VM | Libvirt | attacker-lab | Identifiants |
|---|---|---|---|
| `kali` | `attacker-kali` | `.10` | `kali` / `kali` |
| `parrot` | `attacker-parrot` | `.11` | ceux de l'image virtuelle officielle (documentation Parrot) |

```bash
labctl attacker up                  # crée ce qui manque (images officielles, empreintes vérifiées)
labctl attacker start kali          # un seul poste à la fois suffit en général (4 Go chacun)
labctl core internet on attacker kali   # mises à jour, outils (fermé par défaut)
labctl pentest up                   # branche les postes que pentest accueille (attach:)
```

- Sur un réseau cible, la carte n'a **ni route par défaut ni DNS** : Internet et la résolution passent par
  `attacker-lab`, dont le DNS résout aussi les noms des labs où le poste est branché (`web.pentest.<racine>`, `<racine>` = domaine racine du lab).
- Les postes ne routent pas d'un réseau à l'autre : les labs cibles restent isolés entre eux et de l'Internet.
- Clavier : Kali prend celui de ton poste par l'agent invité QEMU (`labctl attacker keyboard kali [fr]`) ; l'image
  de Parrot n'a pas d'agent invité : clavier à régler dans sa session.
- Parrot démarre en **UEFI** (`hardware: {firmware: efi}`) : ses snapshots se prennent VM arrêtée, et elle ignore
  l'arrêt ACPI tant qu'une session est ouverte (`labctl attacker stop parrot --force` si besoin).
