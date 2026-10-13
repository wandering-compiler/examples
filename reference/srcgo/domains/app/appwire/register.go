// Package appwire assembles the `app` domain's hand-written business
// tier onto a gRPC server. It is the ONE place that knows about every
// module's business package, which is why it lives at domain level:
// a module's `business` package must never import a sibling's.
//
// The split from `appdeps` is what keeps that acyclic — modules import
// the dependency types from `appdeps`, and `appwire` imports the
// modules.
package appwire

import (
	"google.golang.org/grpc"

	taskspb "github.com/wandering-compiler/examples/reference/gen/pb/domains/app/workspace/tasks"
	userspb "github.com/wandering-compiler/examples/reference/gen/pb/domains/app/workspace/users"

	"github.com/wandering-compiler/examples/reference/domains/app/appdeps"
	tasksbiz "github.com/wandering-compiler/examples/reference/domains/app/modules/tasks/business"
	usersbiz "github.com/wandering-compiler/examples/reference/domains/app/modules/users/business"
)

// Register attaches both facade implementations plus the
// in-domain TasksBusiness handler to the gRPC server.
// Helper kept here so the domain main.go stays a thin
// assembly script.
//
// Authentication is owned by the activated `auth` plugin
// (registered_as `auth`): its AuthService (Authenticate /
// SignIn / SignUp / WhoAmI) is staged into and served by the
// app-storage bundle. The gateway's token-type router invokes
// the plugin's `Authenticate` per request; this business
// bundle no longer carries a hand-written auth backend.
func Register(srv *grpc.Server, deps appdeps.Deps) {
	userspb.RegisterUsersServiceServer(srv, usersbiz.NewUsers(deps))
	taskspb.RegisterTasksServiceServer(srv, tasksbiz.NewTasks(deps))
	taskspb.RegisterTasksBusinessServer(srv, tasksbiz.NewTasksBusiness(deps))
	// the project's own URL_TOKEN auth method, served from
	// this bundle alongside the facades. The auth PLUGIN keeps serving
	// bearer auth from app-storage; the two coexist on one REST
	// surface because w17 owns the auth contract, not an
	// implementation of it.
	tasksbiz.RegisterPublicLinkAuth(srv, deps)
	// The BASIC-scheme ops method, which unlike the URL_TOKEN one above
	// POPULATES `permission_ids` — so the generated gate enforces a
	// decision this project's own code made. Three auth methods on one
	// surface, each reached by its own Authorization scheme.
	tasksbiz.RegisterOpsAuth(srv, deps)
}
