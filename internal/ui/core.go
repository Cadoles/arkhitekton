package ui

import (
	"embed"
	"fmt"
	"sync"

	schema "forge.cadoles.com/vcarroy/arkhitekton/sdk/ui"
)

//go:embed resources/*.yaml
var coreFiles embed.FS

// CoreResources are the dashboard's own screens, menus and widgets.
func CoreResources() ([]schema.Resource, error) {
	return ReadFiles(coreFiles, "resources/*.yaml")
}

// Core returns the core's resources. They are built in and checked by the tests: one that does
// not read is the dashboard's bug, and it panics.
var Core = sync.OnceValue(func() []schema.Resource {
	resources, err := CoreResources()
	if err != nil {
		panic(fmt.Sprintf("the core's interface resources: %v", err))
	}
	return resources
})
