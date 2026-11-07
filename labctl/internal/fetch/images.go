package fetch

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
)

// Image est une image cloud (cloud-init) du catalogue.
type Image struct {
	Name   string
	URL    string
	Sums   string // fichier d'empreintes publié par la distribution
	Sig    string // signature détachée de Sums, si l'éditeur en publie une (voir sig.go)
	Key    string // clé embarquée qui doit l'avoir signée (keys/<Key>.asc)
	Algo   string // sha256 ou sha512
	OSInfo string // identifiant libosinfo (métadonnée de la VM)
}

// Images : catalogue des images utilisables par « labctl vm create -i ». Ajouter une entrée = une URL,
// son fichier d'empreintes et un identifiant libosinfo.
var Images = map[string]Image{
	"debian12": {URL: "https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2",
		Sums: "https://cloud.debian.org/images/cloud/bookworm/latest/SHA512SUMS", Algo: "sha512", OSInfo: "http://debian.org/debian/12"},
	"debian13": {URL: "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-generic-amd64.qcow2",
		Sums: "https://cloud.debian.org/images/cloud/trixie/latest/SHA512SUMS", Algo: "sha512", OSInfo: "http://debian.org/debian/13"},
	"ubuntu2404": {URL: "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img",
		Sums: "https://cloud-images.ubuntu.com/noble/current/SHA256SUMS", Sig: "https://cloud-images.ubuntu.com/noble/current/SHA256SUMS.gpg", Key: "ubuntu", Algo: "sha256", OSInfo: "http://ubuntu.com/ubuntu/24.04"},
	"ubuntu2604": {URL: "https://cloud-images.ubuntu.com/resolute/current/resolute-server-cloudimg-amd64.img",
		Sums: "https://cloud-images.ubuntu.com/resolute/current/SHA256SUMS", Sig: "https://cloud-images.ubuntu.com/resolute/current/SHA256SUMS.gpg", Key: "ubuntu", Algo: "sha256", OSInfo: "http://ubuntu.com/ubuntu/24.04"},
	"rocky9": {URL: "https://dl.rockylinux.org/pub/rocky/9/images/x86_64/Rocky-9-GenericCloud-Base.latest.x86_64.qcow2",
		Sums: "https://dl.rockylinux.org/pub/rocky/9/images/x86_64/CHECKSUM", Sig: "https://dl.rockylinux.org/pub/rocky/9/images/x86_64/CHECKSUM.asc", Key: "rocky", Algo: "sha256", OSInfo: "http://rockylinux.org/rocky/9"},
	"alma9": {URL: "https://repo.almalinux.org/almalinux/9/cloud/x86_64/images/AlmaLinux-9-GenericCloud-latest.x86_64.qcow2",
		Sums: "https://repo.almalinux.org/almalinux/9/cloud/x86_64/images/CHECKSUM", Sig: "https://repo.almalinux.org/almalinux/9/cloud/x86_64/images/CHECKSUM.asc", Key: "alma", Algo: "sha256", OSInfo: "http://almalinux.org/almalinux/9"},
}

// ImageNames renvoie les noms du catalogue, triés.
func ImageNames() []string {
	out := make([]string, 0, len(Images))
	for n := range Images {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// FetchImage télécharge l'image name du catalogue dans dst : empreinte attendue lue (et authentifiée par la
// signature de l'éditeur s'il en publie une) avant le téléchargement, fichier vérifié (empreinte, structure) puis
// publié de façon atomique. fresh : ne pas reprendre un téléchargement partiel. Progression écrite sur out.
func FetchImage(name, dst string, fresh bool, out io.Writer) error {
	img, ok := Images[name]
	if !ok {
		return fmt.Errorf("image inconnue : %s", name)
	}
	file := path.Base(img.URL)

	// Empreinte attendue, avant le téléchargement ; fichier d'empreintes authentifié par sa signature si
	// l'éditeur en publie une
	sums, err := Small(img.Sums)
	if err != nil {
		return fmt.Errorf("empreintes de %s : %w", name, err)
	}
	if img.Sig != "" {
		sig, err := Small(img.Sig)
		if err != nil {
			return fmt.Errorf("signature des empreintes de %s : %w", name, err)
		}
		if err := VerifySums(img.Key, sums, sig); err != nil {
			return fmt.Errorf("%s : %w — image refusée", name, err)
		}
		fmt.Fprintf(out, "ok  empreintes de %s signées par l'éditeur (clé %s)\n", name, img.Key)
	}
	want, err := ParseSums(bytes.NewReader(sums), file)
	if err != nil {
		return fmt.Errorf("%s : %w", img.Sums, err)
	}

	part := dst + ".part"
	if fresh {
		os.Remove(part)
	}
	fmt.Fprintf(out, "==> Téléchargement de %s\n", name)
	if err := Download(img.URL, part, out); err != nil {
		return err
	}
	fmt.Fprintf(out, "==> Vérification %s\n", img.Algo)
	got, err := FileHash(part, img.Algo)
	if err != nil {
		return err
	}
	if got != want {
		os.Remove(part)
		return fmt.Errorf("empreinte %s invalide pour %s (fichier supprimé, relancer)", img.Algo, name)
	}
	if _, err := CheckDisk(part); err != nil { // structure : aucune référence vers un fichier de l'hôte
		os.Remove(part)
		return err
	}
	if err := os.Chmod(part, 0o644); err != nil {
		return err
	}
	if err := os.Rename(part, dst); err != nil {
		return err
	}
	fmt.Fprintf(out, "ok  %s -> %s (empreinte %s conforme)\n", name, dst, img.Algo)
	return nil
}
