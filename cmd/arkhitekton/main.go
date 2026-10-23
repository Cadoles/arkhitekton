// Command arkhitekton is the default distribution: the dashboard, its built-in plugins and the MSE
// extension, until the latter is built from its own repository.
package main

import (
	"forge.cadoles.com/vcarroy/arkhitekton/app"
	"forge.cadoles.com/vcarroy/arkhitekton/host"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/mse"
)

// Filled in at compile time through -ldflags (see Dockerfile and Makefile).
var (
	version  string
	revision string
)

func main() {
	app.Main(app.Options{
		Version: version, Revision: revision, Extensions: []host.Extension{mse.Extension()},
	})
}
