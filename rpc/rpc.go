// Package rpc is the wire between arkhitekton and a native plugin: a separate program the dashboard
// launches and talks to over gRPC, through hashicorp/go-plugin.
//
// The contract is proto/arkhitekton/plugin/v1/plugin.proto, generated into pluginv1. A plugin
// written in Go uses the extension package and never this one directly; the dashboard uses this one
// to connect.
package rpc

import (
	"context"
	"fmt"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"forge.cadoles.com/vcarroy/arkhitekton/rpc/pluginv1"
)

// ProtocolVersion is the version of the contract. A plugin built against another one is refused at
// the handshake, before any call.
const ProtocolVersion = 1

// Name is the single plugin a native plugin's program serves.
const Name = "arkhitekton"

// Handshake is what the dashboard and the plugin check before talking. The cookie only keeps a
// plugin from being run by hand by mistake; it is not a secret.
var Handshake = goplugin.HandshakeConfig{
	ProtocolVersion:  ProtocolVersion,
	MagicCookieKey:   "ARKHITEKTON_PLUGIN",
	MagicCookieValue: "a1b0d2c5-native-plugin",
}

// Roles a plugin is launched in.
const (
	RoleAPI       = "api"
	RoleWorker    = "worker"
	RoleReprocess = "reprocess"
)

// GRPCPlugin binds the contract to go-plugin. The plugin's program sets Implementation; the
// dashboard leaves it nil and receives a [Connection].
type GRPCPlugin struct {
	goplugin.NetRPCUnsupportedPlugin
	// Implementation builds the plugin's server once the broker is known: Init needs it to reach
	// the dashboard's Host service.
	Implementation func(broker *goplugin.GRPCBroker) pluginv1.PluginServer
}

// GRPCServer registers the plugin's implementation.
func (p *GRPCPlugin) GRPCServer(broker *goplugin.GRPCBroker, server *grpc.Server) error {
	if p.Implementation == nil {
		return fmt.Errorf("no implementation to serve")
	}
	pluginv1.RegisterPluginServer(server, p.Implementation(broker))
	return nil
}

// GRPCClient returns the dashboard's side of the connection.
func (p *GRPCPlugin) GRPCClient(_ context.Context, broker *goplugin.GRPCBroker, conn *grpc.ClientConn) (any, error) {
	return &Connection{Plugin: pluginv1.NewPluginClient(conn), broker: broker}, nil
}

// Connection is a running plugin, seen from the dashboard.
type Connection struct {
	Plugin pluginv1.PluginClient
	broker *goplugin.GRPCBroker
}

// Init offers the plugin the dashboard's Host service and tells it the role it runs in. The Host
// server lives as long as the plugin's process.
func (c *Connection) Init(ctx context.Context, host pluginv1.HostServer, role, pluginID string) error {
	id := c.broker.NextId()
	go c.broker.AcceptAndServe(id, func(options []grpc.ServerOption) *grpc.Server {
		server := grpc.NewServer(options...)
		pluginv1.RegisterHostServer(server, host)
		return server
	})
	_, err := c.Plugin.Init(ctx, &pluginv1.InitRequest{HostBrokerId: id, Role: role, PluginId: pluginID})
	return err
}

// PluginSet is what both sides hand go-plugin.
func PluginSet(plugin *GRPCPlugin) goplugin.PluginSet {
	return goplugin.PluginSet{Name: plugin}
}
