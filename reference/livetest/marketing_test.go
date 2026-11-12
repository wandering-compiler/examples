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
	Events     []stubEvent    `json:"events"`
}

// stubEvent is an event on a contact as the stub keeps it.
type stubEvent struct {
	EventName       string         `json:"event_name"`
	EventProperties map[string]any `json:"event_properties"`
}

// contactUntil polls the stub for the contact keyed by user until done says
// it holds what the test waits for.
//
// The wait is long because of the queue, not the plugin: every test before
// these signs users up, and the plugin takes auth's UserCreated events one at
// a time, a few a second. On 12 November ~190 were still queued when the
// marketing tests started and the queue took ~95 s to empty, so a test's own
// sign-up reached Brevo well after 30 s.
func contactUntil(t *testing.T, user string, done func(stubContact) bool) stubContact {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
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
		for _, e := range c.Events {
			if e.EventName == event {
				return c.Email != "" && e.EventProperties["w17_org_id"] != nil
			}
		}
		return false
	})
	if c.Email != email {
		t.Errorf("contact %s has email %q, want %q (from auth's UserCreated)", user, c.Email, email)
	}
	// The organization is the one the request ran in: this project's users
	// are members of one organization (org_membership), the one bound to the
	// host they registered on. The sign-up was in org-alpha; org-beta
	// (fixtures/app/test.json) is the decoy.
	const orgAlpha, orgBeta = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	for _, e := range c.Events {
		if e.EventName != event {
			continue
		}
		if st, _ := e.EventProperties["w17_status"].(float64); st != 200 {
			t.Errorf("event %s has w17_status %v, want 200", event, e.EventProperties["w17_status"])
		}
		if org, _ := e.EventProperties["w17_org_id"].(string); org != orgAlpha {
			t.Errorf("event %s ran in organization %q, want org-alpha's %s, not org-beta's %s (properties %v)", event, org, orgAlpha, orgBeta, e.EventProperties)
		}
		// The organization travels with the request, so the contact does not
		// carry one (a user belongs to several).
		if _, stale := c.Attributes["W17_ORG_ID"]; stale {
			t.Errorf("contact %s has a W17_ORG_ID attribute %v; the organization is the event's w17_org_id", user, c.Attributes["W17_ORG_ID"])
		}
		// The project leaves auth's membership_kinds off: no account type,
		// and no empty property standing in for one.
		if kind, ok := e.EventProperties["w17_account_type"]; ok {
			t.Errorf("event %s has w17_account_type %v, but the project declares no membership kinds", event, kind)
		}
		// The request's own fields are not sent: the fault's message is
		// marketing-neutral, and still must not appear.
		raw, _ := json.Marshal(e)
		assertAbsent(t, "the Brevo event", raw, append(f.decoys(), f.Message, f.ContactEmail))
	}
}

// With brevo_attributes the same request also dates the contact: the method's
// attribute holds the day (a Brevo date), W17_LAST_ACTIVITY_AT the exact time,
// and the sign-up dates it too (W17_SIGNED_UP_AT, from auth's UserCreated).
// Brevo drops an attribute the account has not created, and so does the
// stub: the attributes being there proves the plugin created them at boot.
func TestMarketing_AMutationDatesTheContact(t *testing.T) {
	email := fmt.Sprintf("live-mkt-attr-%d@reference.example", time.Now().UnixNano())
	before := time.Now().UTC().Add(-time.Minute)
	token := signUpAs(t, email)
	report := reportFault(t, token, newFault(nonce()))
	ev, envelope := sentry.eventFor(t, report)
	user := ev.Tags["user_id"]
	if user == "" {
		t.Fatalf("the error report names no user_id: %.600s", envelope)
	}

	const attr = "W17_USERSSERVICE_REPORTCLIENTFAULT"
	c := contactUntil(t, user, func(c stubContact) bool {
		return c.Attributes[attr] != nil && c.Attributes["W17_LAST_ACTIVITY_AT"] != nil && c.Attributes["W17_SIGNED_UP_AT"] != nil
	})
	after := time.Now().UTC().Add(time.Minute)
	for _, name := range []string{attr, "W17_SIGNED_UP_AT"} {
		day, _ := c.Attributes[name].(string)
		d, err := time.Parse("2006-01-02", day)
		if err != nil || d.After(after) || d.Before(before.Truncate(24*time.Hour)) {
			t.Errorf("contact %s: %s = %v, want today's date as YYYY-MM-DD", user, name, c.Attributes[name])
		}
	}
	last, _ := c.Attributes["W17_LAST_ACTIVITY_AT"].(string)
	if at, err := time.Parse(time.RFC3339, last); err != nil || at.Before(before) || at.After(after) {
		t.Errorf("contact %s: W17_LAST_ACTIVITY_AT = %v, want the time of the request (RFC 3339)", user, c.Attributes["W17_LAST_ACTIVITY_AT"])
	}
}
