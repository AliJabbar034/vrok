// Command vrok shares local files, directories and development servers
// through temporary URLs.
package main

import (
	"os"

	"github.com/AliJabbar034/vrok/internal/cli"
)

// Build metadata, set with -ldflags at release time. See the Makefile.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	os.Exit(cli.Execute(cli.BuildInfo(version, commit, date)))
}
