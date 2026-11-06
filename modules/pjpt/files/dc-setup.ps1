# dc-setup.ps1 — promotion de dc01 en premier contrôleur de la forêt ${var.domain}.
# Lancé par labctl (compte « lab », administrateur local) AVANT le redémarrage : la promotion elle-même ne
# redémarre pas (labctl déclenche le reboot à l'étape suivante). Modèle labctl : les variables var.* sont remplacées à la copie.
$ErrorActionPreference = 'Stop'

Install-WindowsFeature -Name AD-Domain-Services, DNS, GPMC -IncludeManagementTools | Out-Null

# Après la promotion, le compte local « lab » devient un simple utilisateur du domaine et perd l'accès SSH
# (réservé aux administrateurs). Une tâche planifiée, exécutée au prochain démarrage sous le compte
# Administrateur (devenu Administrateur du domaine), le replace dans « Domain Admins » : labctl peut alors se
# reconnecter en SSH pour peupler l'annuaire.
$readd = @'
$ErrorActionPreference = "SilentlyContinue"
for ($i = 0; $i -lt 60; $i++) {
  Import-Module ActiveDirectory
  if (Get-Command Add-ADGroupMember -ErrorAction SilentlyContinue) {
    try { Add-ADGroupMember -Identity "Domain Admins" -Members "lab"; break } catch { }
  }
  Start-Sleep 10
}
schtasks /Delete /TN LabReaddAdmin /F
'@
Set-Content -Path C:\labs\readd-lab.ps1 -Value $readd -Encoding ASCII
schtasks /Create /TN LabReaddAdmin /SC ONSTART /RU Administrator /RP '${var.admin_password}' /RL HIGHEST `
  /TR "powershell -NoProfile -ExecutionPolicy Bypass -File C:\labs\readd-lab.ps1" /F | Out-Null

# Promotion en premier DC de la forêt (DNS inclus). Pas de redémarrage ici : labctl s'en charge.
$dsrm = ConvertTo-SecureString '${var.dsrm_password}' -AsPlainText -Force
Install-ADDSForest `
  -DomainName '${var.domain}' `
  -DomainNetbiosName '${var.netbios}' `
  -SafeModeAdministratorPassword $dsrm `
  -InstallDns:$true `
  -NoRebootOnCompletion:$true `
  -Force:$true
