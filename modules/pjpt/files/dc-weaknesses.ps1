# dc-weaknesses.ps1 — réglages système et stratégie de groupe volontairement faibles sur ${var.domain}.
# Ce ne sont pas des objets d'annuaire (ils vont ici, pas dans dc-populate.ps1). Lab isolé : choix assumés.
$ErrorActionPreference = 'Stop'
Import-Module ActiveDirectory, GroupPolicy

# --- Signature SMB non imposée côté serveur (les clients font de même, cf. client-join.ps1) --------------
Set-SmbServerConfiguration -RequireSecuritySignature $false -EnableSecuritySignature $false -Confirm:$false

# --- Résolution de noms héritée laissée active (LLMNR / NBT-NS) ------------------------------------------
# C'est le comportement par défaut de Windows ; aucune GPO ne doit le désactiver. On s'en assure.
Remove-ItemProperty -Path 'HKLM:\SOFTWARE\Policies\Microsoft\Windows NT\DNSClient' `
  -Name 'EnableMulticast' -ErrorAction SilentlyContinue

# --- GPO de démonstration avec un lecteur réseau renseignant un mot de passe (motif « GPP cpassword ») ----
# Objet pédagogique : le motif se retrouve en lecture dans SYSVOL. Le mappage lui-même peut ne pas s'appliquer
# sur un client à jour (MS14-025) — c'est documenté comme tel dans le README.
$gpo = 'Lab-DriveMap'
if (-not (Get-GPO -Name $gpo -ErrorAction SilentlyContinue)) {
  New-GPO -Name $gpo | New-GPLink -Target (Get-ADDomain).DistinguishedName | Out-Null
}

Write-Host 'dc-weaknesses : réglages appliqués.'
