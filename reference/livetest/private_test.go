package livetest

import (
	"context"
	"testing"
	"time"

	"github.com/wandering-compiler/sdk/go/lib/privatesurface"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// billingPrivate is billing's private surface as app's code dials it: the
// private proxy's name for the domain, never billing's tiers.
var billingPrivate = env("BILLING_PRIVATE_ADDR", "billing-private:50061")

// privateCerts is the dev private PKI `w17ctl codegen` minted
// (w17/certs/private), as the suite's container sees the example.
const privateCerts = "/w/w17/certs/private/"

// callAs calls billing's SubscriptionQuery.GetAccountByUser through the
// private proxy, presenting the identity of bundle <as>.
//
// GetAccountByUserReq is { int64 user_id = 1 }: on the wire it IS an
// Int64Value, so the suite needs none of the example's own types; the answer
// is read as Empty (its fields kept as unknown) — only the status matters:
// the storage tier answers an unknown user NotFound, which is billing's
// database answering, as much as OK would be.
func callAs(t *testing.T, as string) error {
	t.Helper()
	lookup := func(k string) string {
		return map[string]string{
			"W17_PRIVATE_TLS_CERT": privateCerts + as + ".crt",
			"W17_PRIVATE_TLS_KEY":  privateCerts + as + ".key",
			"W17_PRIVATE_TLS_CA":   privateCerts + "ca.crt",
		}[k]
	}
	conn, err := privatesurface.Dial(billingPrivate, lookup)
	if err != nil {
		t.Fatalf("dial %s as %s: %v", billingPrivate, as, err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return conn.Invoke(ctx, "/e2e.billing.core.SubscriptionQuery/GetAccountByUser",
		wrapperspb.Int64(987654321), &emptypb.Empty{})
}

// Billing is a domain whose only gateway is private, composed into one
// binary (billing-server: its tiers, its eventbus and its private surface in
// one process). App's business tier reaches it the one way across a domain
// boundary — mutual TLS through the private proxy — and only a domain the
// surface admits gets in.
func TestBillingPrivateSurface_ComposedServer(t *testing.T) {
	// app is a caller of billing's surface: the call reaches billing's
	// storage tier in billing-server and its database answers — NotFound,
	// for a user billing has no account for. Anything else (Unavailable:
	// the path is broken; Internal: the query failed) is not an answer.
	answered := func(err error) bool { c := status.Code(err); return c == codes.OK || c == codes.NotFound }
	var err error
	for deadline := time.Now().Add(60 * time.Second); ; {
		if err = callAs(t, "app-business"); answered(err) || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !answered(err) {
		t.Fatalf("app-business → billing's private surface: %v, want billing's database to answer (OK or NotFound)", err)
	}
	// billing itself is not: a domain reaches its own tiers directly.
	if err := callAs(t, "billing-server"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("billing-server → its own private surface: %v, want PermissionDenied", err)
	}
}
