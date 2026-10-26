module forge.cadoles.com/vcarroy/arkhitekton/rpc

go 1.26.4

require (
	forge.cadoles.com/vcarroy/arkhitekton/sdk v0.1.0
	github.com/hashicorp/go-plugin v1.8.0
	google.golang.org/grpc v1.84.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/fatih/color v1.13.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/hashicorp/yamux v0.1.2 // indirect
	github.com/mattn/go-colorable v0.1.12 // indirect
	github.com/mattn/go-isatty v0.0.17 // indirect
	github.com/oklog/run v1.1.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260706201446-f0a921348800 // indirect
)

// The sdk beside this module, so that a change to both builds before it is tagged. A module that
// depends on rpc does not follow this replace: it resolves the tag.
replace forge.cadoles.com/vcarroy/arkhitekton/sdk => ../sdk
