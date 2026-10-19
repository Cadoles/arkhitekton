package main

import (
	"fmt"

	"forge.cadoles.com/vcarroy/arkhitekton/internal/catalog"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/config"
	pipelinelib "forge.cadoles.com/vcarroy/arkhitekton/internal/pipelines"
	"forge.cadoles.com/vcarroy/arkhitekton/internal/plugins/builtin"
)

// referenceLibrary is what a project declaring a stack gets installed.
func referenceLibrary() *pipelinelib.Library {
	return pipelinelib.NewLibrary(builtin.Stacks())
}

// loadCatalogue reads the catalogue and binds it to the built-in plugins' capabilities.
func loadCatalogue(cfg config.Config) (*catalog.Catalog, error) {
	registry, err := builtin.Registry()
	if err != nil {
		return nil, fmt.Errorf("built-in plugins: %w", err)
	}
	return catalog.Load(cfg.Tekton.CatalogPath, catalog.WithCapabilities(registry))
}
