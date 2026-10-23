// Command arkhitekton is the default distribution: the dashboard and its built-in plugins.
package main

import "forge.cadoles.com/vcarroy/arkhitekton/app"

// Filled in at compile time through -ldflags (see Dockerfile and Makefile).
var (
	version  string
	revision string
)

func main() {
	app.Main(app.Options{Version: version, Revision: revision})
}
