// register adds the example's relay to the cluster plugin's registry, the
// way an operator does on the Relays page of the admin: a name, the relay's
// management address, and the fingerprint of the certificate it presents.
// It runs once, after the storage bundle is up and its schema is in place,
// and retries until then.
package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	workerspb "github.com/wandering-compiler/examples/jobs/w17/stubs/pb/domains/jobs/workers"
	w17pb "github.com/wandering-compiler/sdk/go/pb/w17"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	pin, err := os.ReadFile(env("RELAY_FINGERPRINT_FILE", "/pins/relay.fingerprint"))
	if err != nil {
		log.Fatalf("register: the relay's fingerprint: %v", err)
	}
	conn, err := grpc.NewClient(env("STORAGE_ADDR", "jobs-storage:50051"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("register: %v", err)
	}
	defer func() { _ = conn.Close() }()
	relays := workerspb.NewRelayMutationClient(conn)
	req := &workerspb.CreateRelayReq{
		Name:            env("RELAY_NAME", "relay-a"),
		Url:             env("RELAY_MANAGEMENT_ADDRESS", "relay:13444"),
		CertFingerprint: strings.TrimSpace(string(pin)),
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := relays.CreateRelay(ctx, req)
		cancel()
		switch {
		case err == nil:
			log.Printf("register: relay %s at %s registered", req.GetName(), req.GetUrl())
			return
		case registered(err):
			log.Printf("register: relay %s was registered already", req.GetName())
			return
		case status.Code(err) == codes.InvalidArgument:
			// The row itself was refused: no retry changes that.
			log.Fatalf("register: %v", err)
		case time.Now().After(deadline):
			log.Fatalf("register: %v", err)
		}
		time.Sleep(2 * time.Second)
	}
}

// registered is whether err is the refusal of a second relay with this name:
// the storage tier answers a unique constraint with InvalidArgument and
// names the violation in the w17 error detail, not in the message.
func registered(err error) bool {
	if status.Code(err) == codes.AlreadyExists {
		return true
	}
	for _, d := range status.Convert(err).Details() {
		if e, ok := d.(*w17pb.ErrorDetail); ok && e.GetCode() == "UNIQUE_VIOLATION" {
			return true
		}
	}
	return false
}
