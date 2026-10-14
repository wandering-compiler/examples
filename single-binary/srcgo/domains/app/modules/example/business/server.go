package business

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	examplepb "github.com/wandering-compiler/examples/single-binary/w17/stubs/pb/domains/app/example"
	"github.com/wandering-compiler/sdk/go/lib/kvfs"
	distxpb "github.com/wandering-compiler/sdk/go/pb/common/distx"
	"github.com/wandering-compiler/sdk/go/service/tx/distx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NotesFacadeServer is the hand-written NotesFacade implementation. Its
// downstreams are the storage clients — reached by direct in-process call
// when this facade runs inside the single app-server — plus the read-only
// store for the `app-uploads` connection, which is how it reaches the
// bytes of a file the gateway wrote.
type NotesFacadeServer struct {
	examplepb.UnimplementedNotesFacadeServer
	noteMut   examplepb.NoteMutationClient
	noteQuery examplepb.NoteQueryClient
	uploads   kvfs.Reader
	distTx    distxpb.W17DistributedTransactionClient

	// self is THIS service as the bundle serves it — wrapped by its emit
	// wrapper — set from what registry.RegisterNotesFacadeServer returned.
	// Composition goes through it so the callee's (w17.event_emit) fires; a
	// call on s itself would skip the wrapper.
	self examplepb.NotesFacadeServer
}

// NewNotesFacadeServer builds the facade from the injected storage
// clients + upload store + transaction client.
func NewNotesFacadeServer(noteMut examplepb.NoteMutationClient, noteQuery examplepb.NoteQueryClient, uploads kvfs.Reader, distTx distxpb.W17DistributedTransactionClient) *NotesFacadeServer {
	return &NotesFacadeServer{noteMut: noteMut, noteQuery: noteQuery, uploads: uploads, distTx: distTx}
}

// CreateNoteCheckedComposed reuses CreateNoteChecked THROUGH s.self — the
// served, emit-wrapped server — so its NoteChecked is published exactly as
// when the gateway calls it.
func (s *NotesFacadeServer) CreateNoteCheckedComposed(ctx context.Context, req *examplepb.CreateNoteCheckedReq) (*examplepb.Note, error) {
	return s.self.CreateNoteChecked(ctx, req)
}

// CreateNoteCheckedInTx composes CreateNoteChecked inside this method's own
// transaction. Its NoteChecked is parked until the transaction commits; a
// "rollback:" title fails the method AFTER the composed call returned, and
// then neither the note nor its event exists.
func (s *NotesFacadeServer) CreateNoteCheckedInTx(ctx context.Context, req *examplepb.CreateNoteCheckedReq) (*examplepb.Note, error) {
	var note *examplepb.Note
	err := distx.Run(ctx, s.distTx, &distxpb.BeginRequest{ConnectionName: "app-postgres"}, func(ctx context.Context) error {
		var err error
		if note, err = s.self.CreateNoteChecked(ctx, req); err != nil {
			return err
		}
		if strings.HasPrefix(req.GetTitle(), "rollback:") {
			return status.Error(codes.Aborted, "rolled back on purpose, after the composed call succeeded")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return note, nil
}

// ReadAttachment resolves the note's attachment row, then reads the
// stored object through the injected store.
//
// The two steps are the whole point: `stored_path` is a storage HANDLE,
// not a filesystem path, and it is passed to Open verbatim. Nothing here
// knows the connection's bucket root (that is the bundle's, from the
// gateway's env var) or the field's bucket_path / sub-bucket rule (that
// is the gateway's, at write time) — which is why this handler survives
// a change to either.
func (s *NotesFacadeServer) ReadAttachment(ctx context.Context, req *examplepb.ReadAttachmentReq) (*examplepb.ReadAttachmentResp, error) {
	row, err := s.noteQuery.GetNoteAttachment(ctx, &examplepb.GetNoteAttachmentReq{NoteId: req.GetNoteId()})
	if err != nil {
		return nil, err
	}
	body, err := s.uploads.Open(ctx, row.GetStoredPath())
	if err != nil {
		// A handle in the column with no object behind it is the shape a
		// mismatched bucket root produces, so it gets its own status
		// rather than a generic Internal: NotFound here means "the row
		// says there is a file and the store disagrees".
		if errors.Is(err, kvfs.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "attachment %q is not in the store", row.GetStoredPath())
		}
		return nil, fmt.Errorf("open attachment: %w", err)
	}
	defer func() { _ = body.Close() }()

	counting := &countingReader{inner: body}
	firstLine := ""
	scanner := bufio.NewScanner(counting)
	if scanner.Scan() {
		firstLine = scanner.Text()
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read attachment: %w", err)
	}
	// Drain the rest so byte_count is the object's size and not just the
	// part the scanner happened to buffer.
	if _, err := io.Copy(io.Discard, counting); err != nil {
		return nil, fmt.Errorf("read attachment: %w", err)
	}
	return &examplepb.ReadAttachmentResp{ByteCount: counting.n, FirstLine: firstLine}, nil
}

// countingReader tallies the bytes actually read from the store.
type countingReader struct {
	inner io.Reader
	n     int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.inner.Read(p)
	c.n += int64(n)
	return n, err
}

// CreateNoteChecked applies a trivial business rule (tags the title) then
// delegates to the storage tier's CreateNote. The downstream call rides
// the injected client — an in-process inprocgrpc.Conn in the app-server,
// so this hop is a direct function dispatch, not a network round-trip.
func (s *NotesFacadeServer) CreateNoteChecked(ctx context.Context, req *examplepb.CreateNoteCheckedReq) (*examplepb.Note, error) {
	return s.noteMut.CreateNote(ctx, &examplepb.CreateNoteReq{
		OwnerId: req.GetOwnerId(),
		Title:   "[checked] " + req.GetTitle(),
		Body:    req.GetBody(),
	})
}
