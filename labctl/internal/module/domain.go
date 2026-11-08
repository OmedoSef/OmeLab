package module

import "strings"

// Domaine d'un module. Il sert à la CA dérivée du module (signée par la racine du lab, contrainte à ce domaine) et
// au DNS de tout le lab (la passerelle répond pour lui). Un module.yaml venu d'un tiers ne doit donc pas pouvoir
// prendre un domaine réel d'Internet : il obtiendrait un certificat valide pour github.com et détournerait ce nom
// dans le lab. Sont admis le domaine racine du lab et ses sous-domaines (choisis par l'opérateur à « labctl init »),
// et les suffixes qui ne sont jamais délégués sur Internet.

// PrivateSuffixes : suffixes réservés à un usage privé (.internal par l'ICANN, .local RFC 6762, .home.arpa
// RFC 8375, .test/.example/.invalid/.localhost RFC 6761) ou d'usage interne répandu jamais délégués (.lan, .lab,
// .corp, .home…).
var PrivateSuffixes = []string{"internal", "local", "home.arpa", "test", "example", "invalid", "localhost",
	"lan", "lab", "corp", "home", "localdomain", "intranet", "private"}

// PrivateDomain indique si domain peut être le domaine d'un module : root ou un de ses sous-domaines, ou un nom
// sous un suffixe de PrivateSuffixes.
func PrivateDomain(domain, root string) bool {
	d, r := strings.ToLower(strings.TrimSuffix(domain, ".")), strings.ToLower(strings.TrimSuffix(root, "."))
	if d == "" {
		return false
	}
	if r != "" && (d == r || strings.HasSuffix(d, "."+r)) {
		return true
	}
	for _, s := range PrivateSuffixes {
		if strings.HasSuffix(d, "."+s) {
			return true
		}
	}
	return false
}
