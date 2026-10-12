// Package business is the assistant domain's hand-written tier: the
// Assistant service. The generated assistant-business bundle builds the
// clients and the registry and hands them to RegisterBusiness.
package business

import (
	assistantbiz "github.com/wandering-compiler/examples/assistant/w17/stubs/business/assistant"
)

// RegisterBusiness registers the Assistant service on the bundle.
func RegisterBusiness(_ *assistantbiz.EnvConfig, registry assistantbiz.HandlerRegistry, clients assistantbiz.ClientSet) error {
	registry.RegisterAssistantServer(&assistant{
		agent:  clients.AgentService(),
		usage:  clients.UsageQuery(),
		scopes: clients.UsageMutation(),
		notes:  clients.NoteQuery(),
	})
	return nil
}
