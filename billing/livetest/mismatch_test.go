package livetest

import (
	"net/http"
	"testing"
)

// succeededTaking is payment_intent.succeeded for an intent that took
// received minor units: what an intent changed at the provider, by something
// other than the plugin, reports.
func succeededTaking(t *testing.T, providerPaymentID string, received int64) []byte {
	t.Helper()
	o := providerObject(t, providerPaymentID)
	o["status"] = "succeeded"
	o["amount_received"] = received
	return event("payment_intent.succeeded", o)
}

// A success for another amount than was asked is held at AMOUNT_MISMATCH with
// what it took, on the real database — not marked succeeded at the asked
// amount — and a copy of the event changes nothing.
func TestWebhook_ASuccessForAnotherAmountIsHeld(t *testing.T) {
	a := signUp(t)
	p := pay(t, a, "19.99")
	body := succeededTaking(t, p.ProviderPaymentID, 1899)
	for i := 0; i < 2; i++ {
		if got := deliverSigned(t, body); got != http.StatusOK {
			t.Fatalf("delivery %d answered %d", i, got)
		}
	}
	if got := paymentOf(t, a, p.ID).Status; got != statusAmountMismatch {
		t.Fatalf("payment is %d, want %d (held)", got, statusAmountMismatch)
	}
	if got := count(t, `SELECT count(*) FROM payment_payment WHERE id = $1 AND received_amount = 18.99 AND received_currency = 'eur'`, p.ID); got != 1 {
		t.Errorf("the held payment does not record what it took (18.99 eur)")
	}
	// A later, matching success moves nothing: the hold is the operator's.
	if got := deliverSigned(t, succeeded(t, p.ProviderPaymentID)); got != http.StatusOK {
		t.Fatalf("a later success answered %d", got)
	}
	if got := paymentOf(t, a, p.ID).Status; got != statusAmountMismatch {
		t.Errorf("a later success moved the held payment to %d", got)
	}
}

// A top-up whose payment took less than was asked grants no credit.
func TestTopUp_AHeldTopUpGrantsNothing(t *testing.T) {
	a := signUp(t)
	var resp struct {
		Payment payment `json:"payment"`
	}
	call(t, a.token, http.MethodPost, "/api/v1/credit/top-ups", map[string]any{
		"amount": "100.00", "currency": "usd", "idempotency_key": "top-up-" + randomHex(6),
	}, http.StatusOK, &resp)
	if got := deliverSigned(t, succeededTaking(t, resp.Payment.ProviderPaymentID, 100)); got != http.StatusOK {
		t.Fatalf("delivery answered %d", got)
	}
	if got := balanceOf(t, a); got != "0" {
		t.Fatalf("balance = %q after a top-up that took 1.00 of 100.00, want 0", got)
	}
	if got := count(t, `SELECT count(*) FROM payment_creditledger WHERE user_id = $1`, a.id); got != 0 {
		t.Errorf("%d ledger rows for a held top-up, want none", got)
	}
	ledgerMatchesBalance(t, a.id)
}
