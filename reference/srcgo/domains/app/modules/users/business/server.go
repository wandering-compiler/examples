// Package business hosts the hand-written facade for the `users`
// module of the `app` domain — `UsersService`. It exposes the module's
// REST surface and composes generated storage calls.
//
// Facade first: every REST endpoint
// flows through a facade EXCEPT the one direct example —
// `UserQuery.GetUser` carries its own `(w17.http)` annotation
// and is proxied by the gateway straight to the storage gRPC
// (no Layer-2).
//
// Implementation pattern: the facade dials the storage tier over gRPC
// (app-storage is its own process), so it holds storage CLIENTS, never
// the bundle's server impl. Streaming methods pump frames between the
// facade's outgoing grpc.ServerStream and the storage's.
//
// The dependency set is `appdeps.Deps`, shared with the domain's other
// modules; registration lives in `appwire`.
package business

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"

	"google.golang.org/grpc/metadata"

	userspb "github.com/wandering-compiler/examples/reference/gen/pb/domains/app/workspace/users"

	"github.com/wandering-compiler/examples/reference/domains/app/appdeps"
)

// UsersServer implements [userspb.UsersServiceServer].
type UsersServer struct {
	userspb.UnimplementedUsersServiceServer
	deps appdeps.Deps
}

// NewUsers builds a UsersService server bound to `deps`.
func NewUsers(deps appdeps.Deps) *UsersServer { return &UsersServer{deps: deps} }

// ListUsers — Q8 bonus showcase. Direct delegation to
// UserQuery storage; proves the full flow:
//   - Q8a optional cursor (proto3-optional, *int64 binding)
//   - Q8b enum field (User.Status in response)
//   - Q8d pagination (LIMIT)
//   - Q8g HTTP query params (gateway decodes ?cursor=...&limit=10)
//
// Forwarding is FIELD BY FIELD, so a field added to the request is
// dropped here until someone adds it — which is what happened to
// `filter`: the storage DQL read `:filter.status_exact`, the gateway
// decoded it off the URL, and this hop silently left it unset, so the
// list came back unfiltered and the positive-case assertion passed
// without the filter ever running. The negative case (0162) is what
// caught it.
func (s *UsersServer) ListUsers(ctx context.Context, req *userspb.ListUsersReq) (*userspb.ListUsersResp, error) {
	storageResp, err := s.deps.UserQuery.ListUsers(ctx, &userspb.ListUsersReq{
		Cursor: req.Cursor,
		Limit:  req.GetLimit(),
		Filter: req.GetFilter(),
	})
	if err != nil {
		return nil, err
	}
	return projectListUsers(storageResp), nil
}

// GetUsersByIds — Q8e IN-clause bind. Direct delegation;
// the storage handler emits PG `= ANY($1)` over the
// repeated id list (`pq.Array(req.GetIds())`).
func (s *UsersServer) GetUsersByIds(ctx context.Context, req *userspb.GetUsersByIdsReq) (*userspb.ListUsersResp, error) {
	storageResp, err := s.deps.UserQuery.GetUsersByIds(ctx, &userspb.GetUsersByIdsReq{
		Ids: req.GetIds(),
	})
	if err != nil {
		return nil, err
	}
	return projectListUsers(storageResp), nil
}

// projectListUsers maps the storage-side ListUsersResp into
// the facade-side shape. Both have the same wire fields;
// the projection handles the per-row UserSummary copy so
// the storage / facade types stay distinct (D-e2e-6 isolation
// — facade types live in `service/`, storage types in
// `users/queries/`).
func projectListUsers(in *userspb.ListUsersResp) *userspb.ListUsersResp {
	out := &userspb.ListUsersResp{}
	for _, u := range in.GetUsers() {
		out.Users = append(out.Users, &userspb.UserSummary{
			Id:          u.GetId(),
			Email:       u.GetEmail(),
			DisplayName: u.GetDisplayName(),
			Status:      u.GetStatus(),
		})
	}
	return out
}

// mdFirst returns the first value of an incoming metadata key, or
// "" when the key never arrived. Deliberately not "" vs absent —
// the gateway middlewares never stamp an empty value (a blank
// header is skipped), so the two cases are the same fact: nothing
// routed a value here.
func mdFirst(md metadata.MD, key string) string {
	if vs := md.Get(key); len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// mdLast returns the LAST value of an incoming metadata key — the one
// a real consumer of a PRECEDENCE-carrying key reads.
//
// Propagated headers arrive once, so mdFirst answers them. The locale
// keys do not: the gateway appends one value per writer, weakest first
// (auto-installed Accept-Language rename → authored header_renames →
// per-endpoint metadata_bindings), and `i18n.LocaleFromContext` /
// `i18n.FormatLocaleFromContext` both take the last. Echoing the first
// would report a value no handler in the stack acts on — which is the
// opposite of what this method exists for.
func mdLast(md metadata.MD, key string) string {
	if vs := md.Get(key); len(vs) > 0 {
		return vs[len(vs)-1]
	}
	return ""
}

// EchoRequestContext reports the gRPC metadata the gateway attached
// to this call.
//
// Nothing else in this project can say what reached the backend.
// Propagated headers never land on a request message and no
// generated handler reads them, so a surface can declare a header
// list, a rename and a default locale and have all three do
// nothing — with every endpoint still answering 200. This method is
// the reader those declarations never had.
//
// Keys are the ones the gateway writes, not the ones the author
// typed: propagation lowercases the header name verbatim
// (`X-Client-Version` → `x-client-version`), while a rename or a
// metadata binding writes the key the declaration NAMES.
func (s *UsersServer) EchoRequestContext(ctx context.Context, _ *userspb.EchoRequestContextReq) (*userspb.EchoRequestContextResp, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	return &userspb.EchoRequestContextResp{
		RequestId:     mdFirst(md, "x-request-id"),
		ClientVersion: mdFirst(md, "x-client-version"),
		TraceOrigin:   mdFirst(md, "w17-trace-origin"),
		EchoChannel:   mdFirst(md, "w17-echo-channel"),
		Language:      mdLast(md, "w17-language"),
	}, nil
}

// IngestSignedWebhook hashes the bytes it was handed.
//
// The handler is deliberately the dumbest thing that can falsify a
// transcode: an upstream that signs its own payload sends the MAC
// out of band, and any rewrite of the body between the wire and
// here — reordered JSON keys, dropped whitespace, a re-encoded
// number — changes the digest and nothing else. So `digest` is the
// assertion, `byte_len` corroborates it with a second independent
// quantity, and `signature_ok` is what a real receiver would branch
// on.
//
// `internal_route` is set on every call precisely so the REST
// surface's `response_omit_fields` has something to remove.
func (s *UsersServer) IngestSignedWebhook(_ context.Context, req *userspb.IngestSignedWebhookReq) (*userspb.IngestSignedWebhookResp, error) {
	sum := sha256.Sum256(req.GetPayload())
	digest := hex.EncodeToString(sum[:])
	return &userspb.IngestSignedWebhookResp{
		Digest:        digest,
		ByteLen:       int32(len(req.GetPayload())),
		SignatureOk:   req.GetSignature() == digest,
		InternalRoute: "app.webhooks.inbound",
	}, nil
}

// StreamDirectoryUpdates replays the public directory card list as
// a stream. Pure pass-through over a single storage read: the
// stream exists so the REST registry has a stream to force onto WS
// and to publish without auth, and a handler doing real work would
// let a failure be blamed on the work.
func (s *UsersServer) StreamDirectoryUpdates(_ *userspb.StreamDirectoryUpdatesReq, stream userspb.UsersService_StreamDirectoryUpdatesServer) error {
	cards, err := s.deps.DirectoryCardQuery.ListDirectoryCards(stream.Context(), &userspb.ListDirectoryCardsReq{})
	if err != nil {
		return err
	}
	for _, c := range cards.GetCards() {
		if err := stream.Send(c); err != nil {
			return err
		}
	}
	return nil
}

// OnDirectoryPlanRetired is the app domain's subscriber handler for
// `directory.plan.*`. The `app-eventbus` bundle dials this binary and
// invokes it once per matched delivery.
//
// It logs and acks. A subscriber that did real work would make a failed
// delivery mean two things at once — the bus lost it, or the work
// failed — and this subscription exists to be a shape, not a workload:
// what it demonstrates is a domain subscribing across its MODULES, an
// `app/users` event handled by an `app/users` service while the same
// bundle carries `app/tasks` on both halves.
//
// Returning nil acks the message. An error hands it back for
// redelivery under the channel's retry policy.
func (s *UsersServer) OnDirectoryPlanRetired(_ context.Context, ev *userspb.DirectoryPlanRetired) (*userspb.DirectoryPlanAck, error) {
	log.Printf("directory plan %d retired", ev.GetPlanId())
	return &userspb.DirectoryPlanAck{}, nil
}
