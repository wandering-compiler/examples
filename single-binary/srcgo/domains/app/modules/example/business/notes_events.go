package business

import (
	"context"
	"strings"

	admin_authpb "github.com/wandering-compiler/examples/single-binary/w17/stubs/pb/domains/app/admin_auth"
	examplepb "github.com/wandering-compiler/examples/single-binary/w17/stubs/pb/domains/app/example"
)

// NotesEventsServer is the hand-written NotesEvents implementation — the
// eventbus handler side of the business tier. In the composed app-server
// the subscriber dispatcher reaches it by direct in-process call, exactly
// like the REST-facing facade does.
type NotesEventsServer struct {
	examplepb.UnimplementedNotesEventsServer
	noteMut examplepb.NoteMutationClient
	noteQry examplepb.NoteQueryClient
}

// NewNotesEventsServer builds the handler from the injected storage clients.
func NewNotesEventsServer(noteMut examplepb.NoteMutationClient, noteQry examplepb.NoteQueryClient) *NotesEventsServer {
	return &NotesEventsServer{noteMut: noteMut, noteQry: noteQry}
}

// OnNoteCreatedRecord records one NoteDelivery row per delivery of
// NoteCreated. Deliberately NOT idempotent: a duplicate delivery leaves a
// second row, which is what the live suite counts.
//
// Failures propagate to the caller (the subscriber dispatcher), which
// turns them into broker-side nack / redelivery per the channel's retry
// config.
func (s *NotesEventsServer) OnNoteCreatedRecord(ctx context.Context, ev *examplepb.NoteCreated) (*examplepb.HandlerAck, error) {
	if _, err := s.noteMut.RecordNoteDelivery(ctx, &examplepb.RecordNoteDeliveryReq{
		NoteId: ev.GetNoteId(),
	}); err != nil {
		return nil, err
	}
	return &examplepb.HandlerAck{Ok: true}, nil
}

// OnNoteCheckedStamp consumes the FACADE-published NoteChecked and stamps
// the note's title.
//
// A title change rather than another NoteDelivery row, on purpose: the
// storage tier's NoteCreated already writes those, so a count cannot say
// WHICH publisher it came from, and the live suite
// reads exactly that count. This observable belongs to one publisher.
//
// It re-reads the note instead of trusting the event's title because
// UpdateNote replaces both columns — the event carries no body, and
// writing an empty one would make the stamp destructive.
func (s *NotesEventsServer) OnNoteCheckedStamp(ctx context.Context, ev *examplepb.NoteChecked) (*examplepb.HandlerAck, error) {
	note, err := s.noteQry.GetNote(ctx, &examplepb.GetNoteReq{NoteId: ev.GetNoteId()})
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(note.GetTitle(), stampPrefix) {
		// Redelivery: the channel may deliver more than once, and a second
		// stamp would grow the title without saying anything new.
		return &examplepb.HandlerAck{Ok: true}, nil
	}
	if _, err := s.noteMut.UpdateNote(ctx, &examplepb.UpdateNoteReq{
		NoteId: ev.GetNoteId(),
		Title:  stampPrefix + note.GetTitle(),
		Body:   note.GetBody(),
	}); err != nil {
		return nil, err
	}
	return &examplepb.HandlerAck{Ok: true}, nil
}

// stampPrefix marks a title the facade-published event reached. The live
// test waits for it; nothing else in the project writes it.
const stampPrefix = "[stamped] "

// OnAuthUserCreatedWelcomeNote writes the new account's welcome note and
// answers with its id, which the declared emit turns into
// WelcomeNoteIssued.
//
// The account id is the auth plugin's UUID and Note.owner_id is a UUID
// column, so it carries straight across — this handler is the seam between
// a plugin's identity model and the project's own rows, which is the
// ordinary reason a project subscribes to a plugin event at all.
func (s *NotesEventsServer) OnAuthUserCreatedWelcomeNote(ctx context.Context, ev *admin_authpb.UserCreated) (*examplepb.WelcomeNoteAck, error) {
	note, err := s.noteMut.CreateNote(ctx, &examplepb.CreateNoteReq{
		OwnerId: ev.GetUserId(),
		Title:   "Welcome",
		Body:    "Your account is ready.",
	})
	if err != nil {
		return nil, err
	}
	return &examplepb.WelcomeNoteAck{NoteId: note.GetId()}, nil
}
