package livetest

// The request stream into a marketing tool: the `marketing` plugin, run in
// the app domain's storage bundle, against a stub of the Brevo API
// (compose.reference.yaml: brevostub). The stub keeps what it was sent and
// answers it back at /_stub/.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

var brevoStub = env("BREVO_STUB_URL", "http://brevostub:8080")

// stubContact is a contact as the stub keeps it.
type stubContact struct {
	ExtID      string         `json:"ext_id"`
	Email      string         `json:"email"`
	Attributes map[string]any `json:"attributes"`
	Events     []struct {
		EventName       string         `json:"event_name"`
		EventProperties map[string]any `json:"event_properties"`
	} `json:"events"`
}

// contactUntil polls the stub for the contact keyed by user until done says
// it holds what the test waits for.
func contactUntil(t *testing.T, user string, done func(stubContact) bool) stubContact {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last stubContact
	var lastRaw string
	for {
		resp, err := http.Get(brevoStub + "/_stub/contacts/" + user)
		if err == nil {
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			lastRaw = fmt.Sprintf("%d %s", resp.StatusCode, raw)
			if resp.StatusCode == http.StatusOK && json.Unmarshal(raw, &last) == nil && done(last) {
				return last
			}
		} else {
			lastRaw = err.Error()
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Brevo stub's contact %s never got there; last answer: %.1500s", user, lastRaw)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// A sign-up becomes a contact keyed by the user's id, with its address
// (auth's UserCreated); the user's next mutation becomes an event on it,
// named after the method, with the record's status.
func TestMarketing_ASignUpAndAMutationReachBrevo(t *testing.T) {
	email := fmt.Sprintf("live-mkt-%d@reference.example", time.Now().UnixNano())
	token := signUpAs(t, email)
	f := newFault(nonce())
	report := reportFault(t, token, f)
	// The user's id is what the contact is keyed by; the error report names
	// it (requestdata_test.go).
	ev, envelope := sentry.eventFor(t, report)
	user := ev.Tags["user_id"]
	if user == "" {
		t.Fatalf("the error report names no user_id: %.600s", envelope)
	}

	const event = "UsersService_ReportClientFault"
	c := contactUntil(t, user, func(c stubContact) bool {
		// The plugin creates W17_ORG_ID in the background, and the stub
		// drops an attribute it does not have yet: wait for it as well.
		_, org := c.Attributes["W17_ORG_ID"]
		for _, e := range c.Events {
			if e.EventName == event {
				return c.Email != "" && org
			}
		}
		return false
	})
	if c.Email != email {
		t.Errorf("contact %s has email %q, want %q (from auth's UserCreated)", user, c.Email, email)
	}
	for _, e := range c.Events {
		if e.EventName != event {
			continue
		}
		if st, _ := e.EventProperties["w17_status"].(float64); st != 200 {
			t.Errorf("event %s has w17_status %v, want 200", event, e.EventProperties["w17_status"])
		}
		// The request's own fields are not sent: the fault's message is
		// marketing-neutral, and still must not appear.
		raw, _ := json.Marshal(e)
		assertAbsent(t, "the Brevo event", raw, append(f.decoys(), f.Message, f.ContactEmail))
	}
	// The organization: this project's users are scoped by tenant_id (auth's
	// tenant_scope), whose tenant is the record's organization. The sign-up
	// was in tenant-alpha; tenant-beta (fixtures/app/test.json) is the decoy.
	const tenantAlpha, tenantBeta = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	if org, _ := c.Attributes["W17_ORG_ID"].(string); org != tenantAlpha {
		t.Errorf("contact %s has W17_ORG_ID %q, want tenant-alpha's %s, not tenant-beta's %s (attributes %v)", user, org, tenantAlpha, tenantBeta, c.Attributes)
	}
}
