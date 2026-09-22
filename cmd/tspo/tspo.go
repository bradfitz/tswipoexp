// The tspo command is the CLI for tswipoexp. It is the real tailscale
// CLI pointed at the LocalAPI that tswipoexp.exe serves on a named
// pipe, so every tailscale subcommand works against the portable
// node.
package main

import (
	"fmt"
	"os"

	"tailscale.com/cmd/tailscale/cli"
)

func main() {
	args := os.Args[1:]
	if !hasSocketFlag(args) {
		args = append([]string{"--socket=" + localAPISocket()}, args...)
	}
	if err := cli.Run(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func hasSocketFlag(args []string) bool {
	for _, a := range args {
		if a == "--socket" || a == "-socket" || len(a) > 9 && (a[:9] == "--socket=" || a[:8] == "-socket=") {
			return true
		}
	}
	return false
}
