package business

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	workpb "github.com/wandering-compiler/examples/jobs/w17/stubs/pb/domains/jobs/work"
	workerspb "github.com/wandering-compiler/examples/jobs/w17/stubs/pb/domains/jobs/workers"
	"github.com/wandering-compiler/examples/jobs/workapi"
)

// ticketHeader is where a relay looks for the ticket the control plane
// granted. It is the cluster plugin's contract with its relays.
const ticketHeader = "w17-cluster-ticket"

// maxWaitSeconds bounds how long a job waits in a relay's queue for a free
// worker before the caller is told the pool is busy.
const maxWaitSeconds = 30

type jobs struct {
	workpb.UnimplementedJobsServer

	cluster workerspb.ClusterServiceClient
}

// Shout runs one job: ask the pool for a slot, send the job to the relay the
// slot is on with the ticket, and return the worker's answer.
//
// A ticket the relay no longer redeems (TICKET_NOT_REDEEMABLE: its claim
// window ran out before this call reached the relay — a stalled machine, a
// slow first dial) is not a refusal of the caller. The work never started, so
// it is asked for once more with a NEW slot: a ticket works once, and a retry
// needs its own. Any other refusal, and a second expiry, goes back as it came.
func (j *jobs) Shout(ctx context.Context, req *workpb.ShoutReq) (*workpb.ShoutResp, error) {
	resp, err := j.shoutOnce(ctx, req)
	if relayReason(err) == ticketNotRedeemable {
		resp, err = j.shoutOnce(ctx, req)
	}
	return resp, err
}

// shoutOnce is one slot, one ticket, one call.
func (j *jobs) shoutOnce(ctx context.Context, req *workpb.ShoutReq) (*workpb.ShoutResp, error) {
	grant, err := j.slot(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(grant.GetAddress(),
		grpc.WithTransportCredentials(credentials.NewTLS(pinned(grant.GetCertFingerprint()))))
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "jobs: the relay cannot be reached: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// The ticket is the only thing that admits this call at the relay, and it
	// works once: a retry needs a new slot.
	call := metadata.AppendToOutgoingContext(ctx, ticketHeader, grant.GetTicket())
	answer, worker, err := workapi.Shout(call, conn, req.GetText())
	if err != nil {
		return nil, err
	}
	return &workpb.ShoutResp{Text: answer, Worker: worker}, nil
}

// ticketNotRedeemable is the relay's reason for a ticket it no longer takes:
// expired, already spent, or never issued.
const ticketNotRedeemable = "TICKET_NOT_REDEEMABLE"

// relayReason is the reason the relay gave for refusing a call (the
// google.rpc.ErrorInfo it attaches); "" when there is none.
func relayReason(err error) string {
	if err == nil {
		return ""
	}
	for _, d := range status.Convert(err).Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}
	return ""
}

// slot holds the ScheduleTask stream until the control plane grants a slot.
// Queued events in between are the caller's place in a relay's queue.
func (j *jobs) slot(ctx context.Context) (*workerspb.TaskGranted, error) {
	stream, err := j.cluster.ScheduleTask(ctx, &workerspb.ScheduleTaskReq{Label: "shout", MaxWaitSeconds: maxWaitSeconds})
	if err != nil {
		return nil, err
	}
	for {
		ev, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil, status.Error(codes.Unavailable, "jobs: the pool closed the request without a slot")
		}
		if err != nil {
			return nil, err
		}
		if g := ev.GetGranted(); g != nil {
			return g, nil
		}
	}
}

// pinned trusts exactly the certificate whose SHA-256 the control plane
// handed out with the grant. The relay's certificate is self-issued, so
// there is no authority to check it against; the fingerprint is the check.
// VerifyConnection, not VerifyPeerCertificate: it also runs on a resumed
// session.
func pinned(fingerprint string) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // replaced by the pin below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("jobs: the relay presented no certificate")
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if got := hex.EncodeToString(sum[:]); got != fingerprint {
				return fmt.Errorf("jobs: the relay's certificate is not the one the control plane named")
			}
			return nil
		},
	}
}
