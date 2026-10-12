package livetest

import (
	"context"
	"sync"
	"testing"
	"time"

	workerspb "github.com/wandering-compiler/examples/jobs/w17/stubs/pb/domains/jobs/workers"
)

// slowJob keeps a worker busy for two seconds (worker/main.go).
const slowJob = "[slow] job"

// twoSlowJobs runs two slow jobs at once and returns the workers that ran
// them.
func twoSlowJobs(t *testing.T, token string) []string {
	t.Helper()
	got := make([]string, 2)
	var wg sync.WaitGroup
	for i := range got {
		wg.Go(func() {
			w, err := shout(token, slowJob)
			if err != nil {
				t.Errorf("job %d: %v", i, err)
			}
			got[i] = w
		})
	}
	wg.Wait()
	return got
}

// Two jobs at once on a pool of two one-slot workers run on both.
func TestPool_ConcurrentJobsUseBothWorkers(t *testing.T) {
	got := twoSlowJobs(t, signUp(t))
	if got[0] == got[1] {
		t.Fatalf("both jobs ran on %q; a one-slot worker took two at once", got[0])
	}
}

// A third job on a full pool waits for a slot and then runs: it is queued,
// not refused, and it finishes only after one of the first two has.
func TestPool_AFullPoolQueuesTheNextJob(t *testing.T) {
	token := signUp(t)
	start := time.Now()
	var wg sync.WaitGroup
	for i := range 3 {
		wg.Go(func() {
			if _, err := shout(token, slowJob); err != nil {
				t.Errorf("job %d: %v", i, err)
			}
		})
	}
	wg.Wait()
	// Two slots, three two-second jobs: the third starts when one ends.
	if took := time.Since(start); took < 3500*time.Millisecond {
		t.Fatalf("three jobs on two slots took %v; the third cannot have waited", took)
	}
}

// A ticket admits one call. The relay forgets it the moment it is redeemed,
// so a replay finds nothing.
func TestTicket_AdmitsOneCall(t *testing.T) {
	g := grant(t)
	if answer, err := redeem(g, g.GetTicket()); err != nil || answer != "TICKET" {
		t.Fatalf("first use: %q %v", answer, err)
	}
	_, err := redeem(g, g.GetTicket())
	if reason := refusalReason(err); reason != "TICKET_NOT_REDEEMABLE" {
		t.Fatalf("replay: %v (reason %q), want TICKET_NOT_REDEEMABLE", err, reason)
	}
}

// A ticket nobody claims within the relay's ticket TTL (5s here) is refused,
// and its slot goes back to the pool: two jobs at once run on both workers
// again, where a slot still held would leave them one.
func TestTicket_AnUnclaimedTicketExpires(t *testing.T) {
	g := grant(t)
	time.Sleep(6 * time.Second)
	if _, err := redeem(g, g.GetTicket()); err == nil {
		t.Fatal("a ticket used after its TTL was admitted")
	}
	if got := twoSlowJobs(t, signUp(t)); got[0] == got[1] {
		t.Fatalf("both jobs ran on %q; the expired ticket's slot did not come back", got[0])
	}
}

// A banned worker gets no more work: the next placement tells the relay,
// and every job runs on the other worker. Lifting the ban brings it back.
func TestBan_StopsWorkReachingTheWorker(t *testing.T) {
	// A ban is recorded under the operator who made it, read from the
	// verified identity the gateway hands every call (x-w17-user). The suite
	// plays the gateway here, for an account it signed up.
	operator, _ := signUpAccount(t)
	ctx := asSignedIn(context.Background(), operator)
	// The registry learns about attached workers from a sweep of the relays
	// (the Workers page of the admin runs the same).
	if _, err := cluster.CheckWorkers(ctx, &workerspb.CheckWorkersReq{}); err != nil {
		t.Fatalf("CheckWorkers: %v", err)
	}
	list, err := workers.ListWorkers(ctx, &workerspb.ListWorkersReq{})
	if err != nil {
		t.Fatalf("ListWorkers: %v", err)
	}
	var banned string
	for _, w := range list.GetWorkers() {
		if w.GetName() == "worker-a" {
			banned = w.GetId()
		}
	}
	if banned == "" {
		t.Fatalf("worker-a is not in the registry: %v", list.GetWorkers())
	}
	if _, err := cluster.BanWorkers(ctx, &workerspb.DecideWorkersActionReq{Ids: []string{banned}}); err != nil {
		t.Fatalf("BanWorkers: %v", err)
	}
	t.Cleanup(func() {
		if _, err := cluster.UnbanWorkers(ctx, &workerspb.DecideWorkersActionReq{Ids: []string{banned}}); err != nil {
			t.Errorf("UnbanWorkers: %v", err)
		}
	})

	token := signUp(t)
	// The first placement after the ban carries it to the relay. Two jobs at
	// once would use both workers (TestPool_ConcurrentJobsUseBothWorkers);
	// with worker-a banned both run on worker-b, one after the other.
	got := twoSlowJobs(t, token)
	for i, w := range got {
		if w != "worker-b" {
			t.Errorf("job %d ran on %q, want only worker-b while worker-a is banned", i, w)
		}
	}
}
