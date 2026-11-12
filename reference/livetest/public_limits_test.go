package livetest

// The gateway's per-address limit on the methods that take no credential,
// with the limit on (run.sh's second phase, PUBLIC_LIMITS_ON=1: a second
// gateway on the stack, GATEWAY_URL, with the test overlay's zeros left out). Each limited
// method has its own bucket per client address, at what its endpoint declares,
// and a method that declares none is not limited:
//
//   - a page every visitor loads (GetHostOrg, PUBLIC_LIMIT_NONE) is never
//     refused, however often one address — an office, a conference — loads it;
//   - wrong passwords from one address are refused at the sign-in's threshold
//     (FAILED_ONLY, the default 30 a minute with a burst of 10);
//   - and that spends nothing of another method's budget: a registration from
//     the same address still goes through.
//
// Every request of this file comes from one address, the suite's container.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

func limitsOn(t *testing.T) {
	t.Helper()
	if os.Getenv("PUBLIC_LIMITS_ON") != "1" {
		t.Skip("the gateway's public limit is off in the main run (compose.test.yaml); run.sh's second phase turns it on")
	}
}

// landing loads the page a white-label sign-in shows n times and fails on any
// answer but 200.
func landing(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		resp, err := http.Get(gateway + "/api/v1/auth/hosts/org-alpha.reference.test")
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("load %d of the host lookup: %d %s — a read every page calls was refused", i+1, resp.StatusCode, raw)
		}
	}
}

// waitForGateway waits for the second gateway run.sh has just started to
// answer — a test stack may build it from source as it starts, which takes a
// while. The host lookup is not limited, so waiting spends nothing.
func waitForGateway(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	for {
		resp, err := http.Get(gateway + "/api/v1/auth/hosts/org-alpha.reference.test")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the gateway with the limit on did not answer within 4 min: %v", err)
		}
		time.Sleep(time.Second)
	}
}

func TestPublicLimits_EachMethodItsOwnBucket(t *testing.T) {
	limitsOn(t)
	waitForGateway(t)

	// An office loading the sign-in page all day: far past any threshold.
	landing(t, 200)

	email := fmt.Sprintf("limits-%d@reference.example", time.Now().UnixNano())
	signUpAs(t, email)

	// Wrong passwords until the gateway refuses: the burst (10) answers 401
	// and then 429. The bucket refills half a token a second, so a slow run
	// may get one or two more before the refusal — never fewer, never 429
	// first.
	wrong := 0
	for {
		body, _ := json.Marshal(map[string]string{"email": email, "password": "not-the-password"})
		req, err := http.NewRequest(http.MethodPost, gateway+"/api/v1/auth/login", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, _, err := api.Mint(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			if resp.Header.Get("Retry-After") == "" {
				t.Error("429 without Retry-After")
			}
			break
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong password %d: %d %s, want 401 until the limit", wrong+1, resp.StatusCode, raw)
		}
		if wrong++; wrong > 12 {
			t.Fatalf("%d wrong passwords from one address and no 429: the sign-in is not limited", wrong)
		}
	}
	if wrong < 10 {
		t.Fatalf("refused after %d wrong passwords, want the burst of 10 first", wrong)
	}

	// The sign-in's bucket is empty; the others are untouched.
	signUpAs(t, fmt.Sprintf("limits-after-%d@reference.example", time.Now().UnixNano()))
	landing(t, 50)
}
