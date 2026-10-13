// a hand-written URL_TOKEN auth method, living beside the
// activated `auth` plugin rather than inside it.
//
// This file is the answer to "does a project have to fork the plugin to
// support a credential shape it doesn't offer?" — no. w17 owns the auth
// CONTRACT (AuthReq.headers in, AuthResp with permission_ids + scopes
// out, both parser-enforced) and `auth_methods[].ref` resolves to any
// RPC in the domain. So this is ordinary business code that happens to
// sit in the auth slot, and the plugin keeps serving bearer auth on the
// same surface, unchanged.
//
// What it does is small on purpose: take the capability token the
// gateway extracted from the URL, resolve it to the tenant that owns the
// linked row, and hand that back as a scope. Everything after that is
// the platform's — the scope value is threaded as
// `x-w17-scope-tenant_id` and the storage codegen filters every read by
// it, exactly as it does for a signed-in principal.
//
// The comparison worth keeping in mind is the alternative this replaces:
// an `exclude_auth` route plus a hand-written `WHERE public_token = …`.
// Both contain exactly ONE unscoped lookup. The difference is that here
// it is a single named query on a service declared cross-tenant, its
// output is a scope, and every subsequent read is filtered by the
// platform; there, the unscoped read IS the endpoint and nothing
// downstream is filtered by anything.
package business

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authpb "github.com/wandering-compiler/examples/reference/gen/pb/domains/app/auth"
	taskspb "github.com/wandering-compiler/examples/reference/gen/pb/domains/app/workspace/tasks"

	"github.com/wandering-compiler/sdk/go/lib/restgw"

	"github.com/wandering-compiler/examples/reference/domains/app/appdeps"
)

// PublicLinkAuthServer implements the project's own URL_TOKEN auth
// method. It returns the auth PLUGIN's AuthResp, not a type of its own:
// every auth method on one REST surface must share an output type
// (parser: validateAuthMethodsShareOutputType), because the gateway
// decodes the marshalled auth response through a single concrete Go
// type per request.
type PublicLinkAuthServer struct {
	taskspb.UnimplementedPublicLinkAuthServiceServer
	deps appdeps.Deps
}

// NewPublicLinkAuth builds the capability-link auth backend.
func NewPublicLinkAuth(deps appdeps.Deps) *PublicLinkAuthServer {
	return &PublicLinkAuthServer{deps: deps}
}

// RegisterPublicLinkAuth attaches it to the gRPC server. Kept separate
// from Register so the wiring reads as what it is — an auth backend the
// gateway calls per request, not another business facade.
func RegisterPublicLinkAuth(srv *grpc.Server, deps appdeps.Deps) {
	taskspb.RegisterPublicLinkAuthServiceServer(srv, NewPublicLinkAuth(deps))
}

// StreamPublicNoteUpdates  is the capability link on a stream.
//
// Its READ is the scoped list query — the one whose DQL carries no token
// at all — so what the link's recipient receives is bounded by the scope
// its auth method returned, and by nothing this handler wrote. Same claim
// as the unary sibling, on a connection that outlives the request.
//
// The re-auth watchdog runs in the gateway, not here: it cancels this
// call's context the first time the token stops authenticating, and the
// send loop below ends the way it would on any other cancellation. A
// handler-side revocation check would be a second mechanism that could
// disagree with the first.
// Receiver is TasksServer, not PublicLinkAuthServer: the RPC is declared
// on TasksService (the project's business facade), while the auth method
// below is its own service. Colocated in this file anyway — the two are
// one feature, and splitting them across files by receiver would hide
// that the stream's isolation comes from the scope the auth method
// returns.
func (s *TasksServer) StreamPublicNoteUpdates(req *taskspb.StreamPublicNoteUpdatesReq, stream taskspb.TasksService_StreamPublicNoteUpdatesServer) error {
	notes, err := s.deps.TaskQuery.ListTenantNotesForPublicLink(stream.Context(), &taskspb.ListTenantNotesForPublicLinkReq{
		PublicToken: req.GetPublicToken(),
	})
	if err != nil {
		return err
	}
	for _, n := range notes.GetNotes() {
		if err := stream.Send(&taskspb.PublicNoteUpdate{
			Id:       n.GetId(),
			TenantId: n.GetTenantId(),
			Title:    n.GetTitle(),
		}); err != nil {
			return err
		}
	}
	return nil
}

// AuthenticatePublicLink turns a share-link token into a scoped identity.
func (s *PublicLinkAuthServer) AuthenticatePublicLink(ctx context.Context, req *authpb.AuthReq) (*authpb.AuthResp, error) {
	// The credential arrives under the reserved key the gateway
	// injected after extracting it from the path. Reading it is
	// indistinguishable from reading any other header, which is the
	// property that let this method be written without the platform
	// growing a second auth contract.
	//
	// It cannot be client-supplied: restgw.StripReservedHeadersMiddleware
	// removes any inbound copy of this header on EVERY request, declared
	// or not, before anything else in the chain observes it.
	token := req.GetHeaders()[restgw.CredentialMetadataKey]
	if token == "" {
		// Belt to the gateway's braces. The generated handler already
		// refuses an empty credential before calling here, but the
		// consequence of a blank token reaching the query is specific
		// and bad: `public_token` is NULLable (almost no note has a
		// share link), and one careless comparison away from "" is a
		// query that matches an arbitrary link-less row and hands out
		// its tenant's scope. Two checks, because the failure is silent
		// and total while a duplicated `if` costs nothing.
		return nil, status.Error(codes.Unauthenticated, "missing capability token")
	}

	// The one cross-tenant read this feature introduces, on the service
	// that declares `(w17.db.bypass_scope_service) = "*"`. It has to be
	// unscoped: the request arrived with no identity, so no scope exists
	// yet — producing one is this method's entire job. A scoped version
	// would need its own answer as an input.
	resolved, err := s.deps.TenantOpsQuery.ResolvePublicNoteToken(ctx, &taskspb.ResolvePublicNoteTokenReq{
		PublicToken: token,
	})
	if err != nil {
		// Do not distinguish "no such token" from "revoked" or from a
		// transport failure in the response. To the recipient of a link
		// they are one event, and answering them differently turns the
		// endpoint into an oracle for probing which tokens exist.
		return nil, status.Error(codes.Unauthenticated, "invalid capability token")
	}
	if resolved.GetTenantId() == "" {
		return nil, status.Error(codes.Unauthenticated, "invalid capability token")
	}

	return &authpb.AuthResp{
		// user_id stays empty: a capability link authenticates a LINK,
		// not a person. Nobody signed in, and inventing a principal id
		// would put a fiction into every log line and audit row this
		// request touches.
		//
		// permission_ids stays empty too, and the two public endpoints
		// declare `(w17.acl_endpoint) = false` to match. Capability
		// authz and role authz answer different questions; the bearer's
		// authority here is possession of a token naming one row, and
		// the URL_TOKEN routing already means this principal cannot
		// reach an endpoint that never declared `credential`.
		//
		// scopes is the whole payload. From here on the platform owns
		// isolation: the gateway threads `x-w17-scope-tenant_id` into
		// outgoing metadata and the storage codegen appends the filter
		// to every read — including reads that carry no token in their
		// DQL at all, which is the only place a broken scope would show.
		Scopes: map[string]string{"tenant_id": resolved.GetTenantId()},
	}, nil
}
