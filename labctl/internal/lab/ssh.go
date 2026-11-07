package lab

import (
	"os"
	"path/filepath"
	"strings"
)

// Connexions SSH de labctl vers les VMs et la passerelle. Toutes partent avec ces options, construites ici et
// nulle part ailleurs : les VMs d'un lab peuvent être hostiles (cibles volontairement vulnérables, machines
// compromises pendant un exercice), on ne leur confie rien de plus que le strict nécessaire.
//
//   - -F /dev/null : la configuration SSH de l'utilisateur (~/.ssh/config) n'est jamais lue. Un « Host * » avec
//     ForwardAgent, ProxyJump, LocalCommand… ne s'applique donc pas aux VMs (l'agent SSH n'y est jamais transmis).
//   - une seule clé, explicite (IdentitiesOnly) : celle dont la partie publique est injectée dans les VMs
//     (LABS_SSH_PUBKEY). Les autres clés de l'agent ne sont pas proposées. L'agent reste utilisable pour cette clé
//     (SSH_AUTH_SOCK), y compris si elle est protégée par une phrase de passe.
//   - pas de known_hosts : les IP des labs sont réutilisées d'une VM à l'autre (pas d'épinglage de clé d'hôte).
//   - BatchMode pour tout ce qui est automatique : aucune invite (mot de passe, phrase de passe), on échoue vite.

// SSHPubKey renvoie la clé publique injectée dans les VMs (LABS_SSH_PUBKEY).
func (l *Lab) SSHPubKey() string {
	return l.Setting("LABS_SSH_PUBKEY", filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519.pub"))
}

// SSHIdentity renvoie la clé à présenter : la clé privée à côté de LABS_SSH_PUBKEY si elle existe, sinon la clé
// publique elle-même (ssh cherche alors la clé privée correspondante dans l'agent).
func (l *Lab) SSHIdentity() string {
	pub := l.SSHPubKey()
	if priv := strings.TrimSuffix(pub, ".pub"); priv != pub {
		if _, err := os.Stat(priv); err == nil {
			return priv
		}
	}
	return pub
}

// SSHArgs renvoie les options SSH communes. batch : aucune invite (tout ce qui est automatique) ; false pour une
// session interactive (une phrase de passe peut alors être demandée).
func (l *Lab) SSHArgs(batch bool) []string {
	o := []string{"-F", "/dev/null",
		"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=5",
		// Une session dont le pair ne répond plus est abandonnée (~60 s) au lieu de pendre indéfiniment.
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=4",
		"-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "PermitLocalCommand=no",
		"-o", "IdentitiesOnly=yes", "-i", l.SSHIdentity()}
	if batch {
		o = append(o, "-o", "BatchMode=yes")
	}
	return o
}

// sshArgs : options automatiques (BatchMode) suivies de la cible et du reste (script…).
func (l *Lab) sshArgs(target string, rest ...string) []string {
	return append(append(l.SSHArgs(true), target), rest...)
}
