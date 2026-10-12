package livetest

import (
	"bytes"
	"net/http"
	"sync"
	"testing"
	"time"
)

// A signed payment_intent.succeeded settles the payment.
func TestWebhook_SettlesThePayment(t *testing.T) {
	a := signUp(t)
	p := pay(t, a, "19.99")
	if p.Status != statusRequiresAction {
		t.Fatalf("a new payment is %d, want %d (awaiting confirmation)", p.Status, statusRequiresAction)
	}

	if got := deliverSigned(t, succeeded(t, p.ProviderPaymentID)); got != http.StatusOK {
		t.Fatalf("delivery answered %d", got)
	}
	if got := paymentOf(t, a, p.ID).Status; got != statusSucceeded {
		t.Fatalf("payment is %d after the provider confirmed it, want %d", got, statusSucceeded)
	}
}

// One event delivered many times, at once, changes the payment once and is
// recorded once. The provider retries, and two of its retries can overlap.
func TestWebhook_ConcurrentRedeliveryHasOneEffect(t *testing.T) {
	a := signUp(t)
	p := pay(t, a, "5.00")
	body := succeeded(t, p.ProviderPaymentID)

	const n = 8
	statuses := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { statuses[i] = deliverSigned(t, body) })
	}
	wg.Wait()
	for i, s := range statuses {
		if s != http.StatusOK {
			t.Errorf("delivery %d answered %d; every copy of an authentic event must be acknowledged", i, s)
		}
	}
	if got := paymentOf(t, a, p.ID).Status; got != statusSucceeded {
		t.Fatalf("payment is %d, want %d", got, statusSucceeded)
	}
	if got := count(t, `SELECT count(*) FROM payment_processedwebhookevent WHERE provider_event_id = $1`, eventID(t, body)); got != 1 {
		t.Errorf("one event recorded %d times", got)
	}
}

// A failure after a success is stale: the payment stays succeeded.
func TestWebhook_LateFailureDoesNotUndoASuccess(t *testing.T) {
	a := signUp(t)
	p := pay(t, a, "7.00")
	if got := deliverSigned(t, succeeded(t, p.ProviderPaymentID)); got != http.StatusOK {
		t.Fatalf("success answered %d", got)
	}
	o := providerObject(t, p.ProviderPaymentID)
	o["status"] = "requires_payment_method"
	if got := deliverSigned(t, event("payment_intent.payment_failed", o)); got != http.StatusOK {
		t.Fatalf("late failure answered %d; it is authentic and must be acknowledged", got)
	}
	if got := paymentOf(t, a, p.ID).Status; got != statusSucceeded {
		t.Fatalf("payment is %d after a late failure, want it still %d", got, statusSucceeded)
	}
}

// Only the provider can settle a payment: a delivery that is not signed with
// the secret, is signed too long ago, or was changed after signing is
// refused and changes nothing.
func TestWebhook_UnauthenticDeliveriesChangeNothing(t *testing.T) {
	a := signUp(t)
	p := pay(t, a, "3.00")
	body := succeeded(t, p.ProviderPaymentID)
	now := time.Now().Unix()

	tampered := bytes.Replace(body, []byte(`"succeeded"`), []byte(`"succeeded" `), 1)
	cases := map[string]struct {
		body []byte
		sig  string
	}{
		"unsigned":              {body, ""},
		"wrong secret":          {body, signature(body, "whsec_someone_else", now)},
		"signed too old":        {body, signature(body, webhookSecret, now-10*60)},
		"changed after signing": {tampered, signature(body, webhookSecret, now)},
	}
	for name, c := range cases {
		if got := deliver(t, c.body, c.sig); got != http.StatusUnauthorized {
			t.Errorf("%s: answered %d, want 401", name, got)
		}
	}
	if got := paymentOf(t, a, p.ID).Status; got != statusRequiresAction {
		t.Fatalf("payment is %d after unauthentic deliveries, want it unchanged (%d)", got, statusRequiresAction)
	}
}

// The signature covers the exact bytes the provider sent, so the gateway must
// hand the body to the plugin byte for byte: whitespace, key order and
// non-ASCII text included.
func TestWebhook_BodyArrivesByteForByte(t *testing.T) {
	a := signUp(t)
	p := pay(t, a, "4.00")
	compact := succeeded(t, p.ProviderPaymentID)
	var spaced bytes.Buffer
	spaced.WriteString("{\n  \"note\": \"Zaplaceno ✓ — děkujeme\",\n")
	spaced.Write(compact[1:])
	spaced.WriteString("\n")
	if got := deliverSigned(t, spaced.Bytes()); got != http.StatusOK {
		t.Fatalf("a correctly signed body with whitespace and non-ASCII text answered %d", got)
	}
	if got := paymentOf(t, a, p.ID).Status; got != statusSucceeded {
		t.Fatalf("payment is %d, want %d", got, statusSucceeded)
	}
}

// An event for a payment this installation created, arriving before the
// payment's row exists, is refused, so the provider delivers it again later
// instead of the event being lost.
func TestWebhook_EventBeforeItsRowIsRetried(t *testing.T) {
	a := signUp(t)
	p := pay(t, a, "2.00")
	// A second intent with this installation's marks, which no local row
	// names yet: what the provider would deliver if its event outran the
	// plugin's insert.
	o := providerObject(t, p.ProviderPaymentID)
	o["id"] = "pi_notyet" + randomHex(6)
	o["status"] = "succeeded"
	body := event("payment_intent.succeeded", o)
	// 404, not just any refusal: a 401 here would mean the signature was
	// refused and the case under test never ran.
	if got := deliverSigned(t, body); got != http.StatusNotFound {
		t.Fatalf("an event whose row has not landed answered %d, want 404: the provider must redeliver it", got)
	}
	if got := count(t, `SELECT count(*) FROM payment_processedwebhookevent WHERE provider_event_id = $1`, eventID(t, body)); got != 0 {
		t.Fatalf("the refused event was recorded as processed; a redelivery would be skipped")
	}
}
