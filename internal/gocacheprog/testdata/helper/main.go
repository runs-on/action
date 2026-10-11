// Command helper runs gocacheprog.Run, so the tests drive the real go command
// through it without building the whole action.
package main

import (
	"os"

	"github.com/runs-on/action/internal/gocacheprog"
)

func main() {
	share := len(os.Args) > 1 && os.Args[1] == gocacheprog.ShareTestResultsFlag
	if err := gocacheprog.Run(os.Stdin, os.Stdout, share); err != nil {
		os.Exit(1)
	}
}
