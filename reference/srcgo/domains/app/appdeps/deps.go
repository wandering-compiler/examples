// Package appdeps holds the dependency set the `app` domain's
// hand-written business tier is constructed from. It sits at domain
// level because every module's business package binds the SAME set —
// the domain main.go builds one instance and hands it to each.
//
// It is a LEAF: it declares types and nothing else, so a module's
// `business` package can import it without any module importing
// another. Wiring (which servers get registered) lives in `appwire`,
// which imports the module packages; keeping the two apart is what
// stops the import cycle.
package appdeps

import (
	"context"

	cachepb "github.com/wandering-compiler/examples/reference/gen/pb/domains/app/cache"
	taskspb "github.com/wandering-compiler/examples/reference/gen/pb/domains/app/workspace/tasks"
	userspb "github.com/wandering-compiler/examples/reference/gen/pb/domains/app/workspace/users"
	billingpb "github.com/wandering-compiler/examples/reference/gen/pb/domains/billing/core"

	"github.com/wandering-compiler/sdk/go/service/tx/txregistry"
)

// Deps is the set of in-process dependencies the business
// servers need. The domain main.go constructs a single
// instance and passes it in.
//
// Storage refs are the typed gRPC server interfaces (NOT
// `*Server` concrete types) so tests can stub them freely.
// Billing clients are real gRPC clients to the `billing`
// domain's PRIVATE surface — another domain is reached through
// its gateway, never its tiers — so the calls go over the wire
// (mutual TLS) even from inside the same compose stack.
// Billing clients may be nil in deploys that opt out of the
// billing tier (the self-hosted scenario): the facade's
// quota / usage paths short-circuit when nil and behave as
// if the user is on the unlimited tier.
type Deps struct {
	UserQuery userspb.UserQueryClient
	// DirectoryCardQuery backs the public directory feed the
	// facade streams. Read-only, and the only storage dependency
	// StreamDirectoryUpdates has.
	DirectoryCardQuery userspb.DirectoryCardQueryClient
	UserMutation       userspb.UserMutationClient
	AuditLogMutation   userspb.AuditLogMutationClient
	TaskQuery          taskspb.TaskQueryClient
	TaskMutation       taskspb.TaskMutationClient
	// TenantOpsQuery is the declared cross-tenant surface
	// (`(w17.db.bypass_scope_service) = "*"`). The
	// capability-link auth backend is its only consumer here: it
	// resolves a share token to the tenant that owns the linked row,
	// which is by definition a lookup no scope can narrow — the
	// request arrives with no identity, and producing one is the job.
	TenantOpsQuery taskspb.TenantOpsQueryClient
	// SessionCacheMutation — Redis-backed session store on the
	// `cache` connection . A gRPC client to app-storage
	// like the rest; the cache module's storage handlers run in
	// the app-storage process.
	SessionCacheMutation  cachepb.SessionCacheMutationClient
	BillingQueryClient    billingpb.SubscriptionQueryClient
	BillingMutationClient billingpb.SubscriptionMutationClient
	// Registry coordinates cross-call transactions. The co-host
	// binary backs it with a distx gRPC client to app-storage; an
	// in-memory txregistry.Memory satisfies the same shape for
	// in-process setups.
	Registry TxCoordinator
}

// TxCoordinator is the cross-call transaction coordinator the
// facade uses to make a multi-storage-call mutation atomic:
// Begin → thread the w17-tx-id header into each storage call →
// Commit (or Rollback on error). Storage runs in its own process,
// so the co-host binary backs this with a distx gRPC client; the
// in-memory txregistry.Memory satisfies the same shape. Signatures
// mirror txregistry.Memory so the facade call sites are
// coordinator-agnostic.
type TxCoordinator interface {
	Begin(ctx context.Context, opts txregistry.BeginOptions) (string, error)
	Commit(txID string) error
	Rollback(txID string) error
}
