# dc-populate.ps1 — peuplement de l'annuaire ${var.domain} : unités d'organisation, utilisateurs, comptes de
# service et quelques relations de droits volontairement mal réglées, telles que les étudie le cours PJPT.
# Lancé par labctl sur dc01 après le redémarrage de promotion. Tout est en clair et faible par choix : lab isolé.
$ErrorActionPreference = 'Stop'

# L'annuaire peut mettre quelques secondes à répondre après le démarrage : on attend.
Import-Module ActiveDirectory
for ($i = 0; $i -lt 60; $i++) {
  try { Get-ADDomain | Out-Null; break } catch { Start-Sleep 5 }
}
$DomainDN = (Get-ADDomain).DistinguishedName

# --- Unités d'organisation -------------------------------------------------
foreach ($ou in @('Users-Lab', 'ServiceAccounts', 'AdminAccounts')) {
  if (-not (Get-ADOrganizationalUnit -Filter "Name -eq '$ou'" -SearchBase $DomainDN -ErrorAction SilentlyContinue)) {
    New-ADOrganizationalUnit -Name $ou -Path $DomainDN -ProtectedFromAccidentalDeletion $false
  }
}
$UsersOU = "OU=Users-Lab,$DomainDN"
$SvcOU   = "OU=ServiceAccounts,$DomainDN"
$AdminOU = "OU=AdminAccounts,$DomainDN"

function New-LabUser($Name, $Sam, $Pwd, $OU) {
  if (-not (Get-ADUser -Filter "SamAccountName -eq '$Sam'" -ErrorAction SilentlyContinue)) {
    New-ADUser -Name $Name -SamAccountName $Sam -UserPrincipalName "$Sam@${var.domain}" `
      -Path $OU -AccountPassword (ConvertTo-SecureString $Pwd -AsPlainText -Force) `
      -Enabled $true -PasswordNeverExpires $true
  }
}

# --- Utilisateurs « bruit » pour une énumération réaliste ------------------
# Un compte sur cinq reçoit un mot de passe faible et réutilisé (surface de pulvérisation de mots de passe) ;
# les autres sont forts et aléatoires.
$First = @('jon','daenerys','tyrion','cersei','jaime','sansa','arya','bran','theon','brienne',
           'davos','varys','petyr','samwell','gilly','podrick','bronn','missandei','margaery','robert')
$Weak  = @('Winter2024!', 'Password123!', 'Summer2025!', 'Welcome1!', 'ChangeMe123!')
for ($i = 0; $i -lt $First.Count; $i++) {
  $sam = $First[$i]
  if ($i % 5 -eq 0) { $pwd = $Weak[($i / 5) % $Weak.Count] }
  else { $pwd = 'Zx9!' + [guid]::NewGuid().ToString().Substring(0, 12) }
  New-LabUser "$sam lab" $sam $pwd $UsersOU
}

# --- Compte de service avec SPN et mot de passe faible (cas « kerberoastable ») --------------------------
New-LabUser 'SQL Service' 'svc_sql' 'SqlService2024!' $SvcOU
setspn -A "MSSQLSvc/dc01.${var.domain}:1433" svc_sql | Out-Null

# --- Compte de service sans pré-authentification Kerberos (cas « AS-REP roastable ») ---------------------
New-LabUser 'Backup Job' 'svc_backup' 'BackupJob2024!' $SvcOU
Set-ADAccountControl -Identity svc_backup -DoesNotRequirePreAuth $true

# --- Comptes aux droits délégués, pour des chemins d'élévation visibles dans un graphe type BloodHound ---
New-LabUser 'Stannis Test' 'stannis' 'Delegated2024!' $AdminOU
New-LabUser 'Melisandre Test' 'melisandre' 'Delegated2024!' $AdminOU

# stannis : contrôle total (GenericAll) sur l'objet ordinateur du DC.
$dc  = Get-ADComputer -Filter "Name -eq '$env:COMPUTERNAME'"
$sid = (Get-ADUser stannis).SID
$acl = Get-Acl "AD:\$($dc.DistinguishedName)"
$acl.AddAccessRule((New-Object DirectoryServices.ActiveDirectoryAccessRule(
  $sid, 'GenericAll', 'Allow')))
Set-Acl "AD:\$($dc.DistinguishedName)" $acl

# melisandre : contrôle total (GenericAll) sur le groupe Domain Admins.
$da   = Get-ADGroup 'Domain Admins'
$sid2 = (Get-ADUser melisandre).SID
$aclDA = Get-Acl "AD:\$($da.DistinguishedName)"
$aclDA.AddAccessRule((New-Object DirectoryServices.ActiveDirectoryAccessRule(
  $sid2, 'GenericAll', 'Allow')))
Set-Acl "AD:\$($da.DistinguishedName)" $aclDA

Write-Host 'dc-populate : annuaire peuplé.'
