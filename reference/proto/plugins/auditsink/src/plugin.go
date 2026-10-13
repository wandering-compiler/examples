// Package auditsink is the plugin's root package — the v4 entry point the
// bundle's generated pluginwire invokes once per activation.
//
// It does nothing, and that is the point. auditsink exists to exercise
// the activation's `connections:` alias map, whose whole effect is on
// WHICH DATABASE the plugin's table is created in; the plugin's runtime
// surface is generated from its DQL like any other. The entry point is
// still mandatory — pluginwire calls RegisterPlugin for every activation
// — which is why a "proto-only" plugin is not actually a shape the
// plugin system has.
package auditsink

import "github.com/wandering-compiler/examples/reference/auditsink/gen"

// RegisterPlugin is the v4 contract. auditsink registers no handler, so
// it accepts the activation's config and returns.
func RegisterPlugin(_ *gen.EnvConfig, _ gen.HandlerRegistry, _ gen.ClientSet) error {
	return nil
}
