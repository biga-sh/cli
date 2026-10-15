// Command biga broadcasts a local directory so others can watch it live.
package main

import (
	"os"

	"github.com/biga-sh/cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
