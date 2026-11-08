package lab

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"
	"unicode/utf16"

	"labs/labctl/internal/fetch"
	"labs/labctl/internal/modfiles"
	"labs/labctl/internal/module"
	"labs/labctl/internal/virt"
	"labs/labctl/internal/vmdef"
)

// VMs Windows (windows: du module.yaml) : installation sans intervention depuis l'ISO de Microsoft.
//
// Trois lecteurs : l'ISO de Windows, l'ISO des pilotes virtio-win, et un petit ISO de réponses généré ici
// (autounattend.xml, script de fin, OpenSSH, clé SSH). Setup trouve autounattend.xml à la racine d'un lecteur,
// charge les pilotes virtio (disque, réseau), installe l'édition demandée et ouvre une première session qui lance
// le script de fin : outils invités virtio et agent QEMU, OpenSSH Server (ta clé pour le compte lab), réseau
// privé, ping. Le script éteint la VM : labctl retire alors les lecteurs, la redémarre et la pilote par SSH
// (files:, run: en PowerShell ; « reboot » redémarre la VM et attend son retour).

// Composants téléchargés par labctl (versions figées, empreintes vérifiées). virtio-win : Fedora ne publie pas
// d'empreinte pour l'ISO, celle-ci est celle de l'ISO obtenu en HTTPS sur fedorapeople.org ; OpenSSH : empreinte
// publiée par GitHub pour la release.
const (
	virtioWinVersion = "0.1.302"
	virtioWinSHA256  = "303f7ae40dad495d6ae474fdc571df58958a4dbc5c37a522d80f9a203867949d"
	openSSHVersion   = "10.4.0.0"
	openSSHSHA256    = "788d8643464a01eb63640a2fd746f9ed23d106b5a901a35f88dc8cd40e446ac9"

	// windowsInstallTimeout : durée maximale de l'installation (jusqu'à l'arrêt par le script de fin)
	windowsInstallTimeout = 90 * time.Minute
)

func virtioWinURL() string {
	return fmt.Sprintf("https://fedorapeople.org/groups/virt/virtio-win/direct-downloads/archive-virtio/virtio-win-%s-1/virtio-win-%s.iso", virtioWinVersion, virtioWinVersion)
}

func openSSHURL() string {
	return fmt.Sprintf("https://github.com/PowerShell/Win32-OpenSSH/releases/download/%sp1-Preview/OpenSSH-Win64-v%s.msi", openSSHVersion, openSSHVersion)
}

// cachedDownload renvoie un fichier du cache des sources, téléchargé et vérifié (SHA-256) si besoin.
func (l *Lab) cachedDownload(name, url, sha256 string, out logWriter) (string, error) {
	path := filepath.Join(l.ImagesDir(), "sources", "cache", name)
	return path, fetch.Cached(url, path, "sha256", sha256, name, out.writer())
}

// verifyWindowsISO contrôle l'empreinte d'un ISO Windows ; le résultat est mémorisé à côté (taille et date),
// pour ne pas relire 5 à 8 Go à chaque création.
func verifyWindowsISO(path, checksum string, out logWriter) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("ISO introuvable : %s", path)
	}
	// windows: iso est un chemin de l'hôte écrit dans le module.yaml : seule une image ISO 9660 est attachée à la
	// VM, jamais un autre fichier (clé, mot de passe…) que lui ferait lire un module malveillant
	if err := isISO9660(path, fi); err != nil {
		return err
	}
	if checksum == "" {
		return nil
	}
	want := strings.TrimPrefix(strings.ToLower(checksum), "sha256:")
	mark := path + ".verified"
	stamp := fmt.Sprintf("%d %d %s\n", fi.Size(), fi.ModTime().Unix(), want)
	if b, err := os.ReadFile(mark); err == nil && string(b) == stamp {
		return nil
	}
	out.logf("==> vérification de %s (sha256)", filepath.Base(path))
	h, err := fetch.FileHash(path, "sha256")
	if err != nil {
		return err
	}
	if h != want {
		return fmt.Errorf("%s : empreinte %s, attendue %s : ISO refusé", filepath.Base(path), h, want)
	}
	_ = modfiles.WriteAtomic(mark, strings.NewReader(stamp), 1<<10) // à côté de l'ISO : jamais par un lien
	out.logf("ok  %s (empreinte sha256 conforme)", filepath.Base(path))
	return nil
}

// isISO9660 vérifie qu'un fichier ordinaire porte la signature ISO 9660 (« CD001 » au secteur 16, que gardent
// aussi les ISO UDF de Windows).
func isISO9660(path string, fi os.FileInfo) error {
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s n'est pas un fichier ordinaire : ISO refusé", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	magic := make([]byte, 5)
	if _, err := f.ReadAt(magic, 0x8001); err != nil || string(magic) != "CD001" {
		return fmt.Errorf("%s n'est pas une image ISO 9660 : ISO refusé", path)
	}
	return nil
}

// windowsLocale : disposition de clavier Windows (InputLocale) et format régional pour une disposition XKB.
func windowsLocale(layout string) (input, user string) {
	switch layout {
	case "fr":
		return "040c:0000040c", "fr-FR"
	case "be":
		return "080c:0000080c", "fr-BE"
	case "ch":
		return "100c:0000100c", "fr-CH"
	case "de":
		return "0407:00000407", "de-DE"
	case "gb":
		return "0809:00000809", "en-GB"
	}
	return "0409:00000409", "en-US"
}

// unattendData : ce que contient autounattend.xml.
type unattendData struct {
	Edition, ComputerName, Password, TimeZone, Input, UserLocale, Org string
	DriverPaths                                                       []string
}

var unattendTmpl = template.Must(template.New("unattend").Funcs(template.FuncMap{"x": vmdef.XMLEsc}).Parse(`<?xml version="1.0" encoding="utf-8"?>
<!-- Généré par labctl : installation sans intervention (windows: du module.yaml) -->
<unattend xmlns="urn:schemas-microsoft-com:unattend" xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State">
  <settings pass="windowsPE">
    <component name="Microsoft-Windows-International-Core-WinPE" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <SetupUILanguage><UILanguage>en-US</UILanguage></SetupUILanguage>
      <InputLocale>{{x .Input}}</InputLocale>
      <SystemLocale>en-US</SystemLocale>
      <UILanguage>en-US</UILanguage>
      <UserLocale>{{x .UserLocale}}</UserLocale>
    </component>
    <component name="Microsoft-Windows-PnpCustomizationsWinPE" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <DriverPaths>{{range $i, $p := .DriverPaths}}
        <PathAndCredentials wcm:action="add" wcm:keyValue="{{$i}}"><Path>{{x $p}}</Path></PathAndCredentials>{{end}}
      </DriverPaths>
    </component>
    <component name="Microsoft-Windows-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <DiskConfiguration>
        <Disk wcm:action="add">
          <DiskID>0</DiskID>
          <WillWipeDisk>true</WillWipeDisk>
          <CreatePartitions>
            <CreatePartition wcm:action="add"><Order>1</Order><Type>EFI</Type><Size>260</Size></CreatePartition>
            <CreatePartition wcm:action="add"><Order>2</Order><Type>MSR</Type><Size>16</Size></CreatePartition>
            <CreatePartition wcm:action="add"><Order>3</Order><Type>Primary</Type><Extend>true</Extend></CreatePartition>
          </CreatePartitions>
          <ModifyPartitions>
            <ModifyPartition wcm:action="add"><Order>1</Order><PartitionID>1</PartitionID><Format>FAT32</Format><Label>System</Label></ModifyPartition>
            <ModifyPartition wcm:action="add"><Order>2</Order><PartitionID>2</PartitionID></ModifyPartition>
            <ModifyPartition wcm:action="add"><Order>3</Order><PartitionID>3</PartitionID><Format>NTFS</Format><Label>Windows</Label><Letter>C</Letter></ModifyPartition>
          </ModifyPartitions>
        </Disk>
      </DiskConfiguration>
      <ImageInstall>
        <OSImage>
          <InstallFrom><MetaData wcm:action="add"><Key>/IMAGE/NAME</Key><Value>{{x .Edition}}</Value></MetaData></InstallFrom>
          <InstallTo><DiskID>0</DiskID><PartitionID>3</PartitionID></InstallTo>
        </OSImage>
      </ImageInstall>
      <UserData>
        <AcceptEula>true</AcceptEula>
        <FullName>{{x .Org}}</FullName>
        <Organization>{{x .Org}}</Organization>
      </UserData>
    </component>
  </settings>
  <settings pass="specialize">
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <ComputerName>{{x .ComputerName}}</ComputerName>
      <TimeZone>{{x .TimeZone}}</TimeZone>
    </component>
    <component name="Microsoft-Windows-TerminalServices-LocalSessionManager" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <fDenyTSConnections>false</fDenyTSConnections>
    </component>
  </settings>
  <settings pass="oobeSystem">
    <component name="Microsoft-Windows-International-Core" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <InputLocale>{{x .Input}}</InputLocale>
      <SystemLocale>en-US</SystemLocale>
      <UILanguage>en-US</UILanguage>
      <UserLocale>{{x .UserLocale}}</UserLocale>
    </component>
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <OOBE>
        <HideEULAPage>true</HideEULAPage>
        <HideOEMRegistrationScreen>true</HideOEMRegistrationScreen>
        <HideOnlineAccountScreens>true</HideOnlineAccountScreens>
        <HideWirelessSetupInOOBE>true</HideWirelessSetupInOOBE>
        <HideLocalAccountScreen>true</HideLocalAccountScreen>
        <ProtectYourPC>3</ProtectYourPC>
      </OOBE>
      <UserAccounts>
        <AdministratorPassword><Value>{{x .Password}}</Value><PlainText>true</PlainText></AdministratorPassword>
        <LocalAccounts>
          <LocalAccount wcm:action="add">
            <Name>lab</Name><DisplayName>lab</DisplayName><Group>Administrators</Group>
            <Password><Value>{{x .Password}}</Value><PlainText>true</PlainText></Password>
          </LocalAccount>
        </LocalAccounts>
      </UserAccounts>
      <AutoLogon>
        <Enabled>true</Enabled><Username>lab</Username><LogonCount>1</LogonCount>
        <Password><Value>{{x .Password}}</Value><PlainText>true</PlainText></Password>
      </AutoLogon>
      <FirstLogonCommands>
        <SynchronousCommand wcm:action="add">
          <Order>1</Order>
          <Description>labctl : fin d'installation</Description>
          <CommandLine>cmd /c "for %d in (D E F G H I J) do if exist %d:\labs-setup.ps1 powershell -NoProfile -ExecutionPolicy Bypass -File %d:\labs-setup.ps1"</CommandLine>
        </SynchronousCommand>
      </FirstLogonCommands>
    </component>
  </settings>
</unattend>
`))

// windowsSetup : script de fin d'installation (première session, compte lab), lancé depuis l'ISO de réponses.
const windowsSetup = `# Généré par labctl : fin d'installation de Windows (outils invités, OpenSSH, réseau), puis arrêt
$ErrorActionPreference = 'Continue'
New-Item -ItemType Directory -Force C:\labs | Out-Null
Start-Transcript -Path C:\labs\setup.log -Append
$drives = Get-PSDrive -PSProvider FileSystem
$me = ($drives | Where-Object { Test-Path (Join-Path $_.Root 'labs-setup.ps1') } | Select-Object -First 1).Root
$vio = ($drives | Where-Object { Test-Path (Join-Path $_.Root 'virtio-win-gt-x64.msi') } | Select-Object -First 1).Root

# Pilotes et outils invités virtio (série, ballon, affichage…), agent invité QEMU
Start-Process msiexec.exe -Wait -ArgumentList "/i ` + "`" + `"$($vio)virtio-win-gt-x64.msi` + "`" + `" /qn /norestart"
Start-Process msiexec.exe -Wait -ArgumentList "/i ` + "`" + `"$($vio)guest-agent\qemu-ga-x86_64.msi` + "`" + `" /qn /norestart"

# OpenSSH Server : la clé de l'hôte pour les administrateurs (compte lab), PowerShell comme shell
Start-Process msiexec.exe -Wait -ArgumentList "/i ` + "`" + `"$($me)OpenSSH-Win64.msi` + "`" + `" /qn /norestart"
New-Item -ItemType Directory -Force C:\ProgramData\ssh | Out-Null
Copy-Item (Join-Path $me 'authorized_keys') C:\ProgramData\ssh\administrators_authorized_keys -Force
icacls.exe C:\ProgramData\ssh\administrators_authorized_keys /inheritance:r /grant '*S-1-5-32-544:F' /grant '*S-1-5-18:F' | Out-Null
New-Item -Path HKLM:\SOFTWARE\OpenSSH -Force | Out-Null
New-ItemProperty -Path HKLM:\SOFTWARE\OpenSSH -Name DefaultShell -Value "$env:WINDIR\System32\WindowsPowerShell\v1.0\powershell.exe" -PropertyType String -Force | Out-Null
Set-Service -Name sshd -StartupType Automatic
Start-Service sshd

# Réseau du lab : profil privé, SSH et ping autorisés ; pas de mise en veille
Get-NetConnectionProfile | Set-NetConnectionProfile -NetworkCategory Private -ErrorAction SilentlyContinue
New-NetFirewallRule -Name labs-ssh -DisplayName 'SSH (labctl)' -Protocol TCP -LocalPort 22 -Action Allow -Profile Any -ErrorAction SilentlyContinue | Out-Null
New-NetFirewallRule -Name labs-ping -DisplayName 'Ping (labctl)' -Protocol ICMPv4 -IcmpType 8 -Action Allow -Profile Any -ErrorAction SilentlyContinue | Out-Null
Enable-NetFirewallRule -DisplayGroup 'Remote Desktop' -ErrorAction SilentlyContinue
powercfg.exe /hibernate off
powercfg.exe /change standby-timeout-ac 0
powercfg.exe /change monitor-timeout-ac 0

New-Item -ItemType File -Force C:\labs\setup-done | Out-Null
Stop-Transcript
shutdown.exe /s /t 5 /c "labctl : installation terminée"
`

// windowsMedia prépare l'installation : ISO vérifié, virtio-win, ISO de réponses ; renseigne s.Install.
func (r *run) windowsMedia(vm string, e module.Effective, s *vmdef.Spec) error {
	w := e.VM.Windows
	lw := logWriter{r}
	if err := verifyWindowsISO(w.ISOPath(), w.Checksum, lw); err != nil {
		return err
	}
	virtio, err := r.l.cachedDownload(fmt.Sprintf("virtio-win-%s.iso", virtioWinVersion), virtioWinURL(), virtioWinSHA256, lw)
	if err != nil {
		return err
	}
	ssh, err := r.l.cachedDownload(fmt.Sprintf("OpenSSH-Win64-v%s.msi", openSSHVersion), openSSHURL(), openSSHSHA256, lw)
	if err != nil {
		return err
	}
	keyPath := r.l.SSHPubKey()
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("clé SSH publique introuvable : %s", keyPath)
	}

	vars := module.VarContext{Module: r.y, VM: vm, Root: r.l.pkiDomain(), Keyboard: r.l.Keyboard()}
	password, err := vars.Expand(w.Password)
	if err != nil {
		return err
	}
	d := unattendData{Edition: w.Edition, Password: password, TimeZone: w.TimeZone, Org: r.l.Org()}
	if d.TimeZone == "" {
		d.TimeZone = "Romance Standard Time"
	}
	d.Input, d.UserLocale = windowsLocale(r.l.Keyboard())
	d.ComputerName = strings.ToUpper(vm)
	if len(d.ComputerName) > 15 {
		d.ComputerName = d.ComputerName[:15]
	}
	// La lettre du lecteur virtio-win n'est pas connue d'avance : chemins essayés sur D: à J:
	for _, letter := range "DEFGHIJ" {
		for _, drv := range []string{"viostor", "vioscsi", "NetKVM"} {
			d.DriverPaths = append(d.DriverPaths, fmt.Sprintf(`%c:\%s\%s\amd64`, letter, drv, w.DriverDir()))
		}
	}
	var xml bytes.Buffer
	if err := unattendTmpl.Execute(&xml, d); err != nil {
		return err
	}

	// ISO de réponses (contient le mot de passe : supprimé après l'installation)
	dir := filepath.Join(r.l.ImagesDir(), "sources", "cache", "answers")
	src := filepath.Join(dir, s.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil { // traversable par qemu (l'ISO y est lu)
		return err
	}
	_ = os.Chmod(dir, 0o755)
	if err := os.MkdirAll(src, 0o700); err != nil { // fichiers de travail, avec le mot de passe : privés
		return err
	}
	defer os.RemoveAll(src)
	msi, err := os.ReadFile(ssh)
	if err != nil {
		return err
	}
	for name, b := range map[string][]byte{
		"autounattend.xml": xml.Bytes(), "labs-setup.ps1": []byte(strings.ReplaceAll(windowsSetup, "\n", "\r\n")),
		"OpenSSH-Win64.msi": msi, "authorized_keys": key,
	} {
		if err := os.WriteFile(filepath.Join(src, name), b, 0o600); err != nil {
			return err
		}
	}
	answer := filepath.Join(dir, s.Name+".iso")
	_ = os.Remove(answer) // reste d'un essai (libvirt l'a donné à qemu : pas réécrivable, mais supprimable)
	if b, err := exec.Command("xorriso", "-as", "mkisofs", "-quiet", "-J", "-R", "-V", "LABS", "-o", answer, src).CombinedOutput(); err != nil {
		return fmt.Errorf("ISO de réponses (xorriso) : %s", strings.TrimSpace(string(b)))
	}
	_ = os.Chmod(answer, 0o644) // lisible par qemu
	s.Install = &vmdef.InstallSpec{ISOs: []string{w.ISOPath(), virtio, answer}}
	return nil
}

// windowsInstall suit l'installation lancée par CreateVM : touche pour démarrer sur le DVD, attente de l'arrêt
// par le script de fin, retrait des lecteurs, redémarrage, attente de SSH, puis files: et run:.
func (r *run) windowsInstall(vm string, e module.Effective) error {
	lv, v := r.libvirtName(vm), r.l.Hypervisor()
	// « Press any key to boot from CD or DVD » : Entrée pendant 30 s (sans effet une fois Setup lancé)
	for i := 0; i < 30; i++ {
		_ = v.SendKey(lv, 28) // KEY_ENTER
		time.Sleep(time.Second)
	}
	r.logf("==> Installation de Windows (%s) : 15 à 40 min ; console : virt-viewer -c qemu:///system %s", e.VM.Windows.Edition, lv)
	start := time.Now()
	for r.l.DomState(lv) == virt.StateRunning {
		if time.Since(start) > windowsInstallTimeout {
			return fmt.Errorf("installation non terminée au bout de %s : voir la console (virt-viewer -c qemu:///system %s)", windowsInstallTimeout, lv)
		}
		time.Sleep(10 * time.Second)
		if m := int(time.Since(start).Minutes()); m > 0 && m%5 == 0 && int(time.Since(start).Seconds())%300 < 10 {
			r.logf("    … %d min", m)
		}
	}
	r.logf("ok  installation terminée (%s)", time.Since(start).Round(time.Minute))
	if err := v.RemoveCDROMs(lv); err != nil {
		return fmt.Errorf("retrait des lecteurs : %w", err)
	}
	_ = os.Remove(filepath.Join(r.l.ImagesDir(), "sources", "cache", "answers", lv+".iso"))
	if err := v.StartDomain(lv); err != nil {
		return err
	}
	r.logf("==> Attente de SSH sur %s…", vm)
	if !r.waitSSH(vm, 600) {
		return fmt.Errorf("%s ne répond pas en SSH (C:\\labs\\setup.log dans la VM)", vm)
	}
	r.logf("ok  %s joignable en SSH (compte %s)", vm, r.l.VMUser())
	return r.windowsProvision(vm, e)
}

// windowsProvision copie les fichiers (modèles remplacés) puis lance les commandes run: en PowerShell ;
// « reboot » redémarre la VM et attend son retour.
func (r *run) windowsProvision(vm string, e module.Effective) error {
	vars := module.VarContext{Module: r.y, VM: vm, Root: r.l.pkiDomain(), Keyboard: r.l.Keyboard()}
	target := r.l.VMUser() + "@" + r.vmIP(vm)
	for _, f := range e.VM.Files {
		src, err := module.SourcePath(r.y.Dir, f.Src) // jamais un fichier de l'hôte hors du module
		if err != nil {
			return fmt.Errorf("%s : %w", vm, err)
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if f.Template {
			text, err := vars.Expand(string(b))
			if err != nil {
				return fmt.Errorf("%s : %w", f.Src, err)
			}
			b = []byte(text)
		}
		dest := strings.ReplaceAll(f.Dest, "/", `\`)
		// Le base64 est intégré au script (pas lu sur l'entrée standard) : lire [Console]::In.ReadToEnd() par SSH
		// attend parfois indéfiniment l'EOF et bloque. Les fichiers de module sont petits (scripts, config).
		script := fmt.Sprintf("New-Item -ItemType Directory -Force (Split-Path '%s') | Out-Null; [IO.File]::WriteAllBytes('%s', [Convert]::FromBase64String('%s'))",
			psQuote(dest), psQuote(dest), base64.StdEncoding.EncodeToString(b))
		if err := r.winSSH(target, script); err != nil {
			return fmt.Errorf("fichier %s : %w", f.Dest, err)
		}
		r.logf("ok  %s -> %s", f.Src, f.Dest)
	}
	for _, c := range e.VM.Run {
		cmd, err := vars.Expand(c)
		if err != nil {
			return err
		}
		if strings.TrimSpace(cmd) == "reboot" {
			r.logf("==> redémarrage de %s", vm)
			if err := r.l.Hypervisor().RebootDomain(r.libvirtName(vm)); err != nil {
				return err
			}
			r.l.sleep(30 * time.Second)
			if !r.waitSSH(vm, 900) {
				return fmt.Errorf("%s ne revient pas après le redémarrage", vm)
			}
			continue
		}
		r.logf("==> %s", firstLine(cmd))
		if err := r.winSSH(target, cmd); err != nil {
			return fmt.Errorf("run « %s » : %w", firstLine(cmd), err)
		}
	}
	return nil
}

// winSSH lance un script PowerShell par SSH (encodé : pas de souci de guillemets) ; sortie dans le journal.
func (r *run) winSSH(target, script string) error {
	u := utf16.Encode([]rune("$ProgressPreference='SilentlyContinue'; $ErrorActionPreference='Stop'\n" + script))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		b[2*i], b[2*i+1] = byte(c), byte(c>>8)
	}
	return r.l.shellTo(target, "powershell -NoProfile -NonInteractive -EncodedCommand "+base64.StdEncoding.EncodeToString(b), nil, r.out)
}

func psQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 100 {
		s = s[:100] + "…"
	}
	return s
}

// logWriter : messages de progression d'un run (téléchargements, vérifications).
type logWriter struct{ r *run }

func (w logWriter) logf(format string, a ...any) { w.r.logf(format, a...) }
func (w logWriter) writer() *runWriter           { return &runWriter{w.r} }

type runWriter struct{ r *run }

func (w *runWriter) Write(b []byte) (int, error) { return w.r.out.Write(b) }
