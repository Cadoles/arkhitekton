// A native plugin for the launcher's tests: built by TestMain, launched by the tests.
//
// FAKE_PLUGIN_MODE picks a misbehaviour: `old-protocol` answers the handshake with another version
// of the contract.
package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	goplugin "github.com/hashicorp/go-plugin"

	"forge.cadoles.com/vcarroy/arkhitekton/rpc"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/extension"
	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
)

func main() {
	if os.Getenv("FAKE_PLUGIN_MODE") == "old-protocol" {
		handshake := rpc.Handshake
		handshake.ProtocolVersion = rpc.ProtocolVersion + 1
		goplugin.Serve(&goplugin.ServeConfig{
			HandshakeConfig: handshake,
			Plugins: rpc.PluginSet(&rpc.GRPCPlugin{Implementation: func(broker *goplugin.GRPCBroker) pluginv1.PluginServer {
				return extension.Server(start, broker)
			}}),
			GRPCServer: goplugin.DefaultGRPCServer,
		})
		return
	}
	extension.Serve(start)
}

func start(_ context.Context, runtime extension.Runtime) (extension.Hooks, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /hello", func(w http.ResponseWriter, r *http.Request) {
		project, err := runtime.Host.ProjectBySlug(r.Context(), "app")
		if err != nil {
			extension.WriteProblem(w, http.StatusBadGateway, "Host", err.Error())
			return
		}
		var environment []string
		for _, variable := range os.Environ() {
			environment = append(environment, strings.SplitN(variable, "=", 2)[0])
		}
		extension.WriteJSON(w, http.StatusOK, map[string]any{
			"role": runtime.Role, "project": project.Label, "environment": environment,
		})
	})
	mux.HandleFunc("GET /crash", func(http.ResponseWriter, *http.Request) { os.Exit(3) })
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(10 * time.Second):
		case <-r.Context().Done():
		}
	})
	return extension.Hooks{Routes: mux}, nil
}
