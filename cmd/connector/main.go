// Command connector runs inside a customer network and forwards Minitest device
// traffic for allowed domains to internal hosts over an outbound-only connection.
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

	fmt.Fprintln(os.Stderr, "minitest-connector: not implemented yet")
	os.Exit(1)
}
