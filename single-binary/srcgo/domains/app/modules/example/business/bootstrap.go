// Package business is the project's hand-written BUSINESS tier for the
// app domain — the only hand-authored code in the w17 model. The
// w17-generated app-business bundle (and, when composed, the single
// app-server) builds the typed EnvConfig + HandlerRegistry + ClientSet
// (the `app` gen package in w17/stubs) and hands them to RegisterBusiness,
// exactly the shape a plugin's RegisterPlugin receives. The injected
// clients reach the storage tier — over the network in a standalone
// app-business, by DIRECT in-process call in the single app-server.
package business

import (
	appbiz "github.com/wandering-compiler/examples/single-binary/w17/stubs/business/app"
)

// RegisterBusiness wires the app facade tier onto the bundle's gRPC
// server via the injected HandlerRegistry, building each facade from the
// injected ClientSet. This example registers two servers: the
// request/response facade (NotesFacade) and the eventbus handler
// (NotesEvents) the subscriber runner dispatches onto. Both delegate to
// the storage NoteMutation.
func RegisterBusiness(cfg *appbiz.EnvConfig, registry appbiz.HandlerRegistry, clients appbiz.ClientSet) error {
	_ = cfg // no env declared for this example
	// AppUploadsStore is the read-only half of the `app-uploads` kvfs
	// connection — injected like any other client, resolved by the bundle
	// from the same bucket root the gateway writes into. Building a driver
	// here by hand would mean this file naming a filesystem path, which is
	// exactly what the connection declaration exists to avoid.
	//
	// The facade keeps the server the registry returns: that is the one
	// served, wrapped by the emit wrapper, and composing through it is what
	// keeps CreateNoteChecked's NoteChecked when another facade method reuses
	// it (CreateNoteCheckedComposed, CreateNoteCheckedInTx).
	facade := NewNotesFacadeServer(clients.NoteMutation(), clients.NoteQuery(), clients.AppUploadsStore(), clients.DistributedTransaction())
	facade.self = registry.RegisterNotesFacadeServer(facade)
	registry.RegisterNotesEventsServer(NewNotesEventsServer(clients.NoteMutation(), clients.NoteQuery()))
	return nil
}
