// Package main is the `app`-domain BUSINESS binary — a CUSTOM,
// hand-written gRPC business server (w17 can also generate a
// <domain>-business bundle instead). It hosts ONLY the hand-written facade
// tier — `UsersService` + `TasksService` + `TasksBusiness` + the
// grant-all `AuthService` (`domains/app/appwire`) — and reaches
// the generated storage handlers + the distributed-transaction sidecar
// (in the separate `app-storage` process) over gRPC.
//
// This is the "use srcgo/gen" path: the binary builds its downstream
// clients from the project's OWN generated `gen/clients` packages —
// per-domain/tier (gen/clients/app/storage, .../billing/storage,
// gen/clients/app/platform), typed against srcgo/gen/pb — and wires them into the
// facade's Deps. It never imports the w17/services bundles — generated ⟂
// hand-written, sharing types over the wire (srcgo/pb ↔ w17/stubs/pb,
// wire-compatible) only.
//
// Boilerplate (observability, otelgrpc span, listen / signal / graceful
// shutdown / reflection / health) is delegated to one
// `sdk/go/service/runtime`.Serve call.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/wandering-compiler/examples/reference/domains/app/appdeps"
	"github.com/wandering-compiler/examples/reference/domains/app/appwire"
	platform "github.com/wandering-compiler/examples/reference/gen/clients/app/platform"
	appstorage "github.com/wandering-compiler/examples/reference/gen/clients/app/storage"
	billingstorage "github.com/wandering-compiler/examples/reference/gen/clients/billing/storage"

	"github.com/wandering-compiler/sdk/go/core/grpcx"
	"github.com/wandering-compiler/sdk/go/lib/i18n"
	distxpb "github.com/wandering-compiler/sdk/go/pb/common/distx"
	w17runtime "github.com/wandering-compiler/sdk/go/service/runtime"
	"github.com/wandering-compiler/sdk/go/service/tx/txregistry"
)

const (
	envListenAddr     = "APP_LISTEN_ADDR"
	envI18nDir        = "APP_I18N_DIR"
	envBillingBackend = "APP_BILLING_BACKEND_ADDR"
	envServiceVersion = "W17_SERVICE_VERSION"
	envEnvironment    = "W17_ENV"
	envSentryDSN      = "W17_SENTRY_DSN"
	envOTelEndpoint   = "W17_OTEL_ENDPOINT"
	defaultAddr       = ":50052"
	serviceName       = "app-business"
)

// distxCoordinator adapts the W17DistributedTransaction gRPC client to
// appdeps.TxCoordinator — Begin/Commit/Rollback over the wire against
// the primary storage bundle. Mirrors txregistry.Memory's signatures so
// the facade stays coordinator-agnostic.
type distxCoordinator struct {
	c distxpb.W17DistributedTransactionClient
}

func (d distxCoordinator) Begin(ctx context.Context, opts txregistry.BeginOptions) (string, error) {
	resp, err := d.c.Begin(ctx, &distxpb.BeginRequest{ConnectionName: opts.ConnectionName})
	if err != nil {
		return "", err
	}
	return resp.GetTxId(), nil
}

func (d distxCoordinator) Commit(txID string) error {
	_, err := d.c.Commit(context.Background(), &distxpb.CommitRequest{TxId: txID})
	return err
}

func (d distxCoordinator) Rollback(txID string) error {
	_, err := d.c.Rollback(context.Background(), &distxpb.RollbackRequest{TxId: txID})
	return err
}

func main() {
	// The app domain's catalogs. The facades' grpcerr.ForUser sentences are
	// translated in THIS process, with the request's locale, so a custom
	// business binary registers them itself — the generated
	// `<domain>-business` bundle does the same at boot. Without it every
	// such sentence stays in its msgid (rest/0692 is the proof). The
	// default is the path from srcgo/, where the stack's `go run` starts.
	i18nDir := os.Getenv(envI18nDir)
	if i18nDir == "" {
		i18nDir = "../w17/languages/app"
	}
	if _, err := i18n.RegisterFS(os.DirFS(i18nDir)); err != nil {
		log.Printf("i18n: register from %s: %v (continuing with msgid fallback)", i18nDir, err)
	}

	// Downstream clients off the project's generated per-domain/tier
	// `clients` packages: each dials a single deployment bundle off a
	// shared grpcx pool (conns lazy + cached per address, otelgrpc trace
	// propagation). A domain's client package never reaches across its
	// boundary; the facade pulls the specific clients it needs.
	pool := grpcx.NewPool()
	pool.EnableHealthWatch(30 * time.Second)
	defer func() { _ = pool.Close() }()

	tls := os.Getenv("W17_CLIENT_TLS") == "true"
	appStore := appstorage.New(appstorage.AddrFromEnv(), pool, tls)
	plat := platform.New(platform.AddrFromEnv(), pool, tls)

	// Facade Deps — app storage-tier clients + the distx coordinator
	// (cross-call atomicity, routed to the `app` domain's OWN storage
	// bundle, whose tx registry holds the connections this facade names).
	// Same shape the facade composed when it dialed by hand.
	facadeDeps := appdeps.Deps{
		UserQuery:            appStore.UserQuery(),
		DirectoryCardQuery:   appStore.DirectoryCardQuery(),
		UserMutation:         appStore.UserMutation(),
		AuditLogMutation:     appStore.AuditLogMutation(),
		TaskQuery:            appStore.TaskQuery(),
		TaskMutation:         appStore.TaskMutation(),
		TenantOpsQuery:       appStore.TenantOpsQuery(),
		SessionCacheMutation: appStore.SessionCacheMutation(),
		Registry:             distxCoordinator{c: plat.DistributedTransaction()},
	}
	// Cross-domain billing — best-effort; `disabled` opts out (the
	// facade short-circuits to the unlimited tier on nil clients).
	if os.Getenv(envBillingBackend) != "disabled" {
		billStore := billingstorage.New(billingstorage.AddrFromEnv(), pool, tls)
		facadeDeps.BillingQueryClient = billStore.SubscriptionQuery()
		facadeDeps.BillingMutationClient = billStore.SubscriptionMutation()
	}

	addr := os.Getenv(envListenAddr)
	if addr == "" {
		addr = defaultAddr
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("%s listening on %s", serviceName, addr)
	err := w17runtime.Serve(ctx, w17runtime.Config{
		ServiceName:     serviceName,
		ServiceVersion:  os.Getenv(envServiceVersion),
		Environment:     os.Getenv(envEnvironment),
		SentryDSN:       os.Getenv(envSentryDSN),
		OTelEndpoint:    os.Getenv(envOTelEndpoint),
		Addr:            addr,
		Reflection:      true,
		ShutdownTimeout: 10 * time.Second,
		RegisterServices: func(s *grpc.Server) error {
			// Hand-written business facade ONLY — UsersService +
			// TasksService + TasksBusiness + AuthService. Storage runs in
			// app-storage, reached via the clients above.
			appwire.Register(s, facadeDeps)
			return nil
		},
	})
	if err != nil {
		log.Fatalf("w17runtime.Serve: %v", err)
	}
}
