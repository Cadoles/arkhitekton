// Command arkhitekton is the dashboard and its built-in plugins; the others are installed
// (docs/06-plugins.md §6.12).
package main

import "github.com/Cadoles/arkhitekton/app"

// Filled in at compile time through -ldflags (see Dockerfile and Makefile).
var (
	version  string
	revision string
)

func main() {
	app.Main(app.Options{Version: version, Revision: revision})
}
