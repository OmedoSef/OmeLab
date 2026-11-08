// Package names définit, une seule fois, les règles des noms que labctl recopie dans des fichiers de configuration
// (dnsmasq, nftables, XML libvirt, cloud-init) : noms de module, de VM, d'alias, de réseau, domaines, adresses MAC.
// Chaque couche les applique — validation du module.yaml, édition par la GUI, génération de la configuration de la
// passerelle, CLI — en défense en profondeur, mais toujours avec la même règle.
package names

import "regexp"

var (
	reLabel  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	reDomain = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
	reMAC    = regexp.MustCompile(`^([0-9a-f]{2}:){5}[0-9a-f]{2}$`)
	reHost   = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?$`)
)

// Label : nom simple en minuscules, chiffres et tirets internes. Module, VM de module, alias, réservation, réseau,
// label DNS.
func Label(s string) bool { return reLabel.MatchString(s) }

// Domain : nom de domaine complet en minuscules, d'au moins deux labels.
func Domain(s string) bool { return reDomain.MatchString(s) }

// MAC : adresse MAC en minuscules (aa:bb:cc:dd:ee:ff).
func MAC(s string) bool { return reMAC.MatchString(s) }

// Host : nom d'une VM créée à la main (labctl vm create), qui devient aussi son nom d'hôte : comme Label, majuscules
// permises.
func Host(s string) bool { return reHost.MatchString(s) }
