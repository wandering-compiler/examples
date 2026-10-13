// OpsAuthServer — the project's own auth method for ops tooling, and the
// answer to "can code we wrote decide what a declarative gate enforces?"
//
// PublicLinkAuthServer next door proves a project can author an auth
// METHOD. It returns no permissions by design: a capability link
// authenticates a LINK, and its two endpoints declare
// `(w17.acl_endpoint) = false`. What was left unproven is the half a
// consumer needs when the identity layer is theirs — code of their own
// computing `permission_ids`, and the GENERATED gate enforcing them.
//
// This method does that. It reads a BASIC credential, decides which of
// this domain's permissions the caller holds, and returns them; the
// generated REST handler then runs
// `acllock.HasPermission(ids, 149 /* users.DirectoryCardQuery.ListDirectoryCards */)`
// against the slice. No hand-written check anywhere on the path.
//
// ⚠️ The ids are PER DOMAIN. A number that means "list directory cards"
// here names a different permission — or nothing — in a sibling domain of
// the same project, so a mapping layer spanning domains must record the
// PAIR (domain, id) and resolve it per domain. See opsPermissionIDs.
package business

import (
	"context"
	"encoding/base64"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authpb "github.com/wandering-compiler/examples/reference/gen/pb/domains/app/auth"
	taskspb "github.com/wandering-compiler/examples/reference/gen/pb/domains/app/workspace/tasks"

	"github.com/wandering-compiler/examples/reference/domains/app/appdeps"
)

// opsPermissionIDs is this project's mapping from an ops capability to
// the permission ids it grants in THIS domain.
//
// The ids are read off `proto/domains/app/w17.lock.acl.proto`, whose
// AclPermission enum is the catalogue. They are written here as numbers
// because authored code has no supported way to read that lock at
// runtime — the generated storage bundle embeds it, the project's own
// module does not — so ops_auth_lock_test.go asserts these against the
// committed lock instead. That test is the reconciliation step any consumer
// holding stored permission references needs: an id is stable against
// re-allocation (retired ones go to `reserved` and are never reused), but
// a RENAME is a removal plus an addition, so both the name and the number
// move at once and nothing in the lock records that they belonged
// together.
// opsServiceAccountID is the principal every ops credential runs as. A
// fixed UUID rather than a per-credential one: the capability is the
// authority here, and minting an id per credential would imply a user
// row that does not exist.
const opsServiceAccountID = "0f5c1a52-7d41-4c8e-9b2a-6e3f0a7c4d15"

var opsPermissionIDs = map[string][]int32{
	// Grants exactly the one endpoint the e2e cases drive, so a
	// regression that widens authority shows up as a 200 where the
	// suite expects a 403.
	"directory-reader": {
		149, // users.DirectoryCardQuery.ListDirectoryCards
	},
	// Authenticates successfully and grants NOTHING. The negative case
	// has to be a caller the gate REFUSES, not a caller the auth method
	// rejects: those are different answers (403 vs 401) and only the
	// first one proves the gate ran.
	"directory-nobody": {},
}

// OpsAuthServer implements the BASIC-scheme auth method.
//
// It returns the auth PLUGIN's AuthResp rather than a type of its own,
// and that is a property of THIS surface, not of the pattern: the parser
// refuses two methods on one surface returning different shapes and
// exempts a surface with a single method. A domain activating no auth
// plugin declares its own response type and is the only method there, so
// the rule is vacuous for it — see ops_auth.proto.
type OpsAuthServer struct {
	taskspb.UnimplementedOpsAuthServiceServer
	deps appdeps.Deps
}

// NewOpsAuth builds the ops auth backend.
func NewOpsAuth(deps appdeps.Deps) *OpsAuthServer { return &OpsAuthServer{deps: deps} }

// RegisterOpsAuth attaches it to the gRPC server. Separate from Register
// for the same reason PublicLinkAuth is: an auth backend the gateway
// calls per request is not another business facade.
func RegisterOpsAuth(srv *grpc.Server, deps appdeps.Deps) {
	taskspb.RegisterOpsAuthServiceServer(srv, NewOpsAuth(deps))
}

// AuthenticateOps resolves a BASIC credential into this domain's
// permission ids.
//
// The credential is `<capability>:<anything>` base64'd, which is enough
// for a fixture and deliberately not a password check — what this proves
// is the wiring from a project's own decision to the generated gate, and
// a real secret comparison here would only add a moving part the suite
// would then have to seed.
func (s *OpsAuthServer) AuthenticateOps(ctx context.Context, req *taskspb.OpsAuthReq) (*authpb.AuthResp, error) {
	capability, ok := basicCapability(req.GetHeaders())
	if !ok {
		// Unauthenticated, NOT permission-denied: nobody was identified.
		// Conflating the two is what makes a 403 unreadable — it would
		// say "you may not" about a caller the gate never saw.
		return nil, status.Error(codes.Unauthenticated, "ops credential missing or malformed")
	}
	ids, known := opsPermissionIDs[capability]
	if !known {
		return nil, status.Error(codes.Unauthenticated, "unknown ops capability")
	}
	return &authpb.AuthResp{
		// The authority the generated gate scans. An empty slice is a
		// legitimate answer — it authenticates the caller and grants
		// nothing, which is the state the negative case drives.
		PermissionIds: ids,
		// A person, unlike a capability link, so this is set: an audit
		// row naming nobody is worse than one naming a service account.
		UserId: opsServiceAccountID,
		// No scope entries: this method grants domain-wide read
		// authority, and inventing a tenant scope would silence the
		// isolation the rest of the suite proves.
	}, nil
}

// basicCapability extracts the username half of an `Authorization: Basic`
// header. Returns false for anything it cannot read, so the caller
// answers Unauthenticated once rather than guessing per failure shape.
//
// The header map arrives lower-cased, which is why the lookup is
// `authorization` and not `Authorization` — reading the canonical Go
// form here would miss every request.
func basicCapability(headers map[string]string) (string, bool) {
	raw := headers["authorization"]
	const prefix = "Basic "
	if len(raw) <= len(prefix) || !strings.EqualFold(raw[:len(prefix)], prefix) {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw[len(prefix):]))
	if err != nil {
		return "", false
	}
	user, _, found := strings.Cut(string(decoded), ":")
	if !found || user == "" {
		return "", false
	}
	return user, true
}
