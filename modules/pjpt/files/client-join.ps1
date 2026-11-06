# client-join.ps1 — joint un poste Windows 11 au domaine ${var.domain} et applique les faiblesses côté client.
# Lancé par labctl sur chaque client ; le redémarrage qui finalise la jonction est déclenché ensuite par labctl.
# Le DC (dc01) est déjà en place : les VMs sont provisionnées dans l'ordre du fichier, DC d'abord.
$ErrorActionPreference = 'Stop'

# --- DNS vers le DC (indispensable pour résoudre le domaine ; la passerelle n'est pas le DNS AD) ----------
$iface = Get-NetAdapter | Where-Object Status -eq 'Up' | Select-Object -First 1
Set-DnsClientServerAddress -InterfaceIndex $iface.ifIndex -ServerAddresses '${vms.dc01.ip}'

# --- Jonction au domaine (identifiants de test, non interactifs) -----------------------------------------
$sec  = ConvertTo-SecureString '${var.admin_password}' -AsPlainText -Force
$cred = New-Object System.Management.Automation.PSCredential("${var.netbios}\Administrator", $sec)
for ($i = 0; $i -lt 30; $i++) {
  try { Add-Computer -DomainName '${var.domain}' -Credential $cred -ErrorAction Stop; break }
  catch { Start-Sleep 10 }
}

# --- Administrateur local IDENTIQUE sur tous les clients (surface de mouvement latéral) ------------------
$admin = Get-LocalUser -Name 'Administrator' -ErrorAction SilentlyContinue
if ($admin) {
  Set-LocalUser -Name 'Administrator' -Password (ConvertTo-SecureString '${var.local_admin_password}' -AsPlainText -Force)
  Enable-LocalUser -Name 'Administrator'
}

# --- Signature SMB non imposée (cohérent avec le serveur) ------------------------------------------------
Set-SmbClientConfiguration -RequireSecuritySignature $false -EnableSecuritySignature $false -Confirm:$false
Set-SmbServerConfiguration -RequireSecuritySignature $false -EnableSecuritySignature $false -Confirm:$false

Write-Host 'client-join : poste prêt, redémarrage à suivre.'
