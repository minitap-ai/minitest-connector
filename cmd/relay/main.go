// Command relay runs in Minitap's cluster. It accepts connector sessions and
// bridges device egress traffic to the right tenant's connector.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/minitap-ai/minitest-connector/internal/version"
)

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.Version)
		return
	}

	fmt.Fprintln(os.Stderr, "minitest-connector relay: not implemented yet")
	os.Exit(1)
}
