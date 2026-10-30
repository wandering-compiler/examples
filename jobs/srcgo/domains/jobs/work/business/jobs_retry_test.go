package business

import (
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func refusedBy(reason string) error {
	st, _ := status.New(codes.PermissionDenied, "relay: refused").WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: "relay"})
	return st.Err()
}

// The retry is decided by the relay's reason, not by the code: only a ticket
// the relay no longer redeems is asked for again. A plain PermissionDenied, or
// another reason, goes back as it came.
func TestRelayReason(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{refusedBy(ticketNotRedeemable), ticketNotRedeemable},
		{refusedBy("WORKER_BANNED"), "WORKER_BANNED"},
		{status.Error(codes.PermissionDenied, "no detail"), ""},
		{nil, ""},
	} {
		if got := relayReason(c.err); got != c.want {
			t.Errorf("relayReason(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}
