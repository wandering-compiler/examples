// Package livetest proves, against the running stack, what the generated
// end-to-end suite cannot: how jobs spread over the pool and queue when it is
// full, that a ticket admits one call and expires when nobody claims it, and
// that a banned worker stops getting work. run.sh brings the stack up with
// `w17ctl test --keep` and runs this package inside the stack's network.
package livetest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	authpb "github.com/wandering-compiler/examples/jobs/w17/stubs/pb/domains/jobs/auth"
	workerspb "github.com/wandering-compiler/examples/jobs/w17/stubs/pb/domains/jobs/workers"
	"github.com/wandering-compiler/examples/jobs/workapi"
)

var (
	gateway     = env("GATEWAY_URL", "http://jobs-gateway:8080")
	storageAddr = env("STORAGE_ADDR", "jobs-storage:50051")

	client = &http.Client{Timeout: 60 * time.Second}

	// The cluster plugin over the storage tier's internal listener: what the
	// project's own code (and an operator's tooling) calls.
	cluster workerspb.ClusterServiceClient
	workers workerspb.WorkerQueryClient
)

// ticketHeader is where the relay looks for a ticket.
const ticketHeader = "w17-cluster-ticket"

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func TestMain(m *testing.M) {
	conn, err := grpc.NewClient(storageAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintln(os.Stderr, "livetest: storage:", err)
		os.Exit(1)
	}
	cluster = workerspb.NewClusterServiceClient(conn)
	workers = workerspb.NewWorkerQueryClient(conn)
	code := m.Run()
	_ = conn.Close()
	os.Exit(code)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func signUp(t *testing.T) string {
	t.Helper()
	_, token := signUpAccount(t)
	return token
}

// signUpAccount signs up a fresh account and returns its id and token.
func signUpAccount(t *testing.T) (userID, token string) {
	t.Helper()
	status, raw, err := do("", "/api/v1/auth/register", map[string]any{
		"email": "live-" + randomHex(6) + "@jobs.example", "password": "e2eSecret!23",
	})
	var resp struct {
		UserID string `json:"user_id"`
		Token  string `json:"token"`
	}
	if err != nil || status != http.StatusOK || json.Unmarshal(raw, &resp) != nil {
		t.Fatalf("sign-up: %d %s %v", status, raw, err)
	}
	return resp.UserID, resp.Token
}

// asSignedIn carries userID's verified identity the way the gateway hands it
// to every call it forwards: the auth method's response, marshaled, under
// x-w17-user.
func asSignedIn(ctx context.Context, userID string) context.Context {
	b, _ := proto.Marshal(&authpb.AuthResp{UserId: userID})
	return metadata.AppendToOutgoingContext(ctx, "x-w17-user", base64.StdEncoding.EncodeToString(b))
}

// refusalReason is the reason a relay gave for refusing a call, from the
// ErrorInfo detail it attaches; "" when there is none.
func refusalReason(err error) string {
	for _, d := range status.Convert(err).Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}
	return ""
}

// do makes one REST POST. It never touches t, so it may run on any
// goroutine.
func do(token, path string, body any) (int, []byte, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequest(http.MethodPost, gateway+path, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

// shout runs one job through the API and returns the worker that ran it.
func shout(token, text string) (worker string, err error) {
	status, raw, err := do(token, "/api/v1/jobs/shout", map[string]any{"text": text})
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("shout: %d %s", status, raw)
	}
	var resp struct {
		Worker string `json:"worker"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", err
	}
	return resp.Worker, nil
}

// grant asks the control plane for one slot, as the Jobs service does.
func grant(t *testing.T) *workerspb.TaskGranted {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream, err := cluster.ScheduleTask(ctx, &workerspb.ScheduleTaskReq{Label: "livetest", MaxWaitSeconds: 20})
	if err != nil {
		t.Fatalf("ScheduleTask: %v", err)
	}
	for {
		ev, err := stream.Recv()
		if err != nil {
			t.Fatalf("ScheduleTask: %v", err)
		}
		if g := ev.GetGranted(); g != nil {
			return g
		}
	}
}

// redeem sends one job to the granted relay with the ticket.
func redeem(g *workerspb.TaskGranted, ticket string) (string, error) {
	conn, err := grpc.NewClient(g.GetAddress(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		InsecureSkipVerify: true, // replaced by the pin below
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("no certificate")
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if hex.EncodeToString(sum[:]) != g.GetCertFingerprint() {
				return errors.New("the relay is not the one the control plane named")
			}
			return nil
		},
	})))
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, ticketHeader, ticket)
	answer, _, err := workapi.Shout(ctx, conn, "ticket")
	return answer, err
}
