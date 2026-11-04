// labctl pilote les labs : une sous-commande par module, l'état global et une GUI web locale.
package main

import (
	"os"

	"labs/labctl/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
