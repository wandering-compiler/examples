// Package gen is the plugin author's compile-time contract with the
// bundle codegen (v4 plugin model). At staging time the bundle
// synthesises a PARALLEL `gen` package holding the same exported names,
// populated for the activation, and rewrites the plugin's self-import to
// it — so the declarations here only have to exist and match by shape.
//
// auditsink declares no `env:`, needs no typed clients and registers no
// handler, so all three are empty. They are still required: the bundle's
// generated pluginwire constructs an EnvConfig, a ClientSet and a
// HandlerRegistry per activation and passes them to RegisterPlugin.
package gen

// EnvConfig — one field per `env:` entry in plugin.yaml. auditsink declares
// none.
type EnvConfig struct{}

// ClientSet — the typed clients a plugin's handlers call. auditsink has no
// handlers; its whole surface is generated from DQL.
type ClientSet interface{}

// HandlerRegistry — the hooks a plugin uses to plug authored service
// implementations onto the bundle's gRPC server. auditsink plugs none.
type HandlerRegistry interface{}
