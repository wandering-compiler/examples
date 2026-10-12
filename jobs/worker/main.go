// worker is one machine of the example's pool. It attaches to a relay with
// the cluster plugin's worker library and serves jobs.Worker/Shout on the
// tunnel the relay calls it back through: the worker dials out and answers on
// the way back, so it never needs to be reachable itself.
//
// Its identity (a key, and the certificate the relay's authority issued for
// it) and the fingerprint of the relay it trusts are prepared in its identity
// directory before it starts; see compose.jobs.yaml.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/wandering-compiler/examples/jobs/workapi"
	"github.com/wandering-compiler/plugins/cluster/lib/workerconn"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

type shouter struct{ name string }

// Shout upper-cases the text. "[slow]" in it makes the job take two seconds,
// so a test can keep a slot busy.
func (s shouter) Shout(ctx context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	if strings.Contains(in.GetValue(), "[slow]") {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	_ = grpc.SetHeader(ctx, metadata.Pairs(workapi.WorkerHeader, s.name))
	return wrapperspb.String(strings.ToUpper(in.GetValue())), nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dir := env("WORKER_IDENTITY_DIR", "/identity")
	pin, err := os.ReadFile(dir + "/relay.fingerprint")
	if err != nil {
		log.Fatalf("worker: the relay's fingerprint: %v", err)
	}
	slots, err := strconv.ParseUint(env("WORKER_SLOTS", "1"), 10, 32)
	if err != nil {
		log.Fatalf("worker: WORKER_SLOTS: %v", err)
	}
	bannedRetry, err := time.ParseDuration(env("WORKER_BANNED_RETRY", "5m"))
	if err != nil {
		log.Fatalf("worker: WORKER_BANNED_RETRY: %v", err)
	}
	name := env("WORKER_NAME", "worker")
	cfg := workerconn.Config{
		RelayAddress:     env("RELAY_ATTACH_ADDRESS", "relay:13446"),
		TunnelAddress:    env("RELAY_TUNNEL_ADDRESS", "relay:13445"),
		RelayFingerprint: strings.TrimSpace(string(pin)),
		IdentityDir:      dir,
		Name:             name,
		Slots:            uint32(slots),
		RetryInterval:    time.Second,
		// A ban can be lifted, so a banned worker keeps asking, slowly.
		BannedRetryInterval: bannedRetry,
	}

	// Attach (and renew) in the background: the relay counts this worker's
	// slots while the attachment holds.
	go func() {
		err := workerconn.Run(ctx, cfg, func(s workerconn.State) { log.Printf("worker %s: %s", name, s) })
		if ctx.Err() == nil {
			log.Fatalf("worker %s: %v", name, err)
		}
	}()

	// Serve jobs on a tunnel, dialling a fresh one whenever it drops.
	for ctx.Err() == nil {
		err := workerconn.ServeTunnel(ctx, cfg, func(s *grpc.Server) { workapi.Register(s, shouter{name: name}) })
		if err != nil && ctx.Err() == nil {
			log.Printf("worker %s: tunnel: %v", name, err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}
