// Command helper runs gocacheprog.Run, so the tests drive the real go command
// through it without building the whole agent.
package main

import (
	"os"

	"github.com/runs-on/action/internal/gocacheprog"
)

func main() {
	if err := gocacheprog.Run(os.Stdin, os.Stdout); err != nil {
		os.Exit(1)
	}
}
