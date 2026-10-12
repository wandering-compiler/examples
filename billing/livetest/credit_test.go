package livetest

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	paymentpb "github.com/wandering-compiler/examples/billing/w17/stubs/pb/domains/billing/payment"
)

func balanceOf(t *testing.T, a account) string {
	t.Helper()
	var resp struct {
		Balance struct {
			Balance string `json:"balance"`
		} `json:"balance"`
	}
	call(t, a.token, http.MethodGet, "/api/v1/billing/credit/balance", nil, http.StatusOK, &resp)
	return resp.Balance.Balance
}

// ledgerMatchesBalance: the balance is always the sum of the ledger.
func ledgerMatchesBalance(t *testing.T, userID string) {
	t.Helper()
	if got := count(t, `SELECT count(*) FROM payment_creditbalance b
		WHERE b.user_id = $1 AND b.balance <> (SELECT COALESCE(SUM(l.delta), 0) FROM payment_creditledger l WHERE l.user_id = $1)`, userID); got != 0 {
		t.Fatal("the balance is not the sum of the ledger")
	}
}

// A top-up is credited once when the provider confirms it, however many
// times and however concurrently the confirmation arrives.
func TestTopUp_CreditedOnceOnConfirmation(t *testing.T) {
	a := signUp(t)
	var resp struct {
		Payment payment `json:"payment"`
	}
	call(t, a.token, http.MethodPost, "/api/v1/credit/top-ups", map[string]any{
		"amount": "10.00", "currency": "usd", "idempotency_key": "top-up-" + randomHex(6),
	}, http.StatusOK, &resp)
	if got := balanceOf(t, a); got != "0" {
		t.Fatalf("balance before the provider confirmed = %q, want 0", got)
	}

	body := succeeded(t, resp.Payment.ProviderPaymentID)
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			got, err := tryDeliverSigned(body)
			if err != nil {
				t.Error(err)
				return
			}
			if got != http.StatusOK {
				t.Errorf("delivery answered %d", got)
			}
		})
	}
	wg.Wait()
	// And a fresh event about the same payment, as after a provider retry
	// with a new event id.
	if got := deliverSigned(t, succeeded(t, resp.Payment.ProviderPaymentID)); got != http.StatusOK {
		t.Fatalf("second event answered %d", got)
	}
	if got := balanceOf(t, a); got != "10.0000" {
		t.Fatalf("balance = %q, want 10.0000 (credited once)", got)
	}
	ledgerMatchesBalance(t, a.id)
}

// Concurrent spends never take a balance below zero: of N spends of 1
// against a balance of K, exactly K succeed, the rest are refused, and the
// balance ends at zero. The guarantee is the database's, so only a real one
// can show it.
func TestSpend_ConcurrentSpendsNeverOverdraw(t *testing.T) {
	a := signUp(t)
	ctx := context.Background()
	const balance, spends = 5, 20
	if _, err := payments.GrantCredit(ctx, &paymentpb.GrantCreditReq{
		UserId: a.id, Amount: "5", Reason: "welcome", IdempotencyKey: "grant-" + randomHex(6),
	}); err != nil {
		t.Fatalf("GrantCredit: %v", err)
	}

	var ok, refused atomic.Int32
	var wg sync.WaitGroup
	for range spends {
		wg.Go(func() {
			_, err := payments.SpendCredit(ctx, &paymentpb.SpendCreditReq{
				UserId: a.id, Amount: "1", Reason: "export", IdempotencyKey: "spend-" + randomHex(6),
			})
			switch status.Code(err) {
			case codes.OK:
				ok.Add(1)
			case codes.FailedPrecondition:
				refused.Add(1)
			default:
				t.Errorf("SpendCredit: %v", err)
			}
		})
	}
	wg.Wait()
	if ok.Load() != balance || refused.Load() != spends-balance {
		t.Fatalf("%d spends succeeded and %d were refused, want %d and %d", ok.Load(), refused.Load(), balance, spends-balance)
	}
	if got := balanceOf(t, a); got != "0.0000" && got != "0" {
		t.Fatalf("balance = %q, want 0", got)
	}
	ledgerMatchesBalance(t, a.id)
}

// A refused spend leaves no trace: no ledger row, the balance unchanged.
func TestSpend_RefusedSpendRollsBack(t *testing.T) {
	a := signUp(t)
	ctx := context.Background()
	if _, err := payments.GrantCredit(ctx, &paymentpb.GrantCreditReq{
		UserId: a.id, Amount: "2", Reason: "welcome", IdempotencyKey: "grant-" + randomHex(6),
	}); err != nil {
		t.Fatalf("GrantCredit: %v", err)
	}
	_, err := payments.SpendCredit(ctx, &paymentpb.SpendCreditReq{
		UserId: a.id, Amount: "3", Reason: "export", IdempotencyKey: "spend-" + randomHex(6),
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("overdrawing spend: %v, want FailedPrecondition", err)
	}
	if got := count(t, `SELECT count(*) FROM payment_creditledger WHERE user_id = $1`, a.id); got != 1 {
		t.Fatalf("ledger rows = %d, want only the grant", got)
	}
	if got := balanceOf(t, a); got != "2.0000" {
		t.Fatalf("balance = %q, want 2.0000", got)
	}
}

// A user's first charges, made at once, share one provider customer.
func TestCharge_ConcurrentFirstChargesShareOneCustomer(t *testing.T) {
	a := signUp(t)
	const n = 6
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			if _, err := tryPay(a, "1.00"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	if got := count(t, `SELECT count(*) FROM payment_customer WHERE user_id = $1`, a.id); got != 1 {
		t.Fatalf("customers = %d, want 1", got)
	}
	if got := count(t, `SELECT count(*) FROM payment_payment p JOIN payment_customer c ON c.id = p.customer_id WHERE c.user_id = $1`, a.id); got != n {
		t.Fatalf("payments = %d, want %d", got, n)
	}
}

// Two people may use the same order number as their idempotency key: each
// gets their own payment.
func TestCharge_SameKeyFromTwoPeopleIsTwoPayments(t *testing.T) {
	a, b := signUp(t), signUp(t)
	key := "order-" + randomHex(4)
	var pa, pb struct {
		Payment payment `json:"payment"`
	}
	body := map[string]any{"amount": "8.00", "currency": "eur", "idempotency_key": key}
	call(t, a.token, http.MethodPost, "/api/v1/billing/payments", body, http.StatusOK, &pa)
	call(t, b.token, http.MethodPost, "/api/v1/billing/payments", body, http.StatusOK, &pb)
	if pa.Payment.ID == pb.Payment.ID {
		t.Fatal("two people's charges are one payment")
	}
	if pa.Payment.ProviderPaymentID == pb.Payment.ProviderPaymentID {
		t.Fatal("two people's charges are one payment at the provider")
	}
}
