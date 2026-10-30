// Package livetest proves, against the running stack, what the payment
// plugin's own unit tests cannot: they run against a fake store and a fake
// provider, so a real database's constraints and transactions, the gateway
// between the provider and the plugin, and concurrent requests are out of
// their reach.
//
// It needs what the generated end-to-end suite cannot do: sign a webhook,
// fire requests concurrently, and read the database. run.sh brings the stack
// up with `w17ctl test --keep` and runs this package inside the stack's
// network.
package livetest

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/wandering-compiler/examples/billing/livetest/device"
	paymentpb "github.com/wandering-compiler/examples/billing/w17/stubs/pb/domains/billing/payment"
)

var (
	gateway       = env("GATEWAY_URL", "http://billing-gateway:8080")
	fakeStripe    = env("FAKESTRIPE_URL", "http://fakestripe:12111")
	storageAddr   = env("STORAGE_ADDR", "billing-storage:50051")
	databaseURL   = env("DATABASE_URL", "postgres://billing:billing@billing-postgres:5432/billing?sslmode=disable")
	webhookSecret = env("WEBHOOK_SECRET", "whsec_example")
	// gatewayOrigin is the gateway's configured public origin, which device
	// proofs name (run.sh reads it from the stack); "" when the stack
	// declares none, and then the proofs name GATEWAY_URL's origin.
	gatewayOrigin = env("GATEWAY_DPOP_ORIGIN", "")

	// api sends the REST calls: with device proofs when the gateway binds
	// tokens to device keys, as Bearer tokens when it does not.
	api *device.Client
	db  *sql.DB
	// payments is PaymentService over the storage tier's internal gRPC
	// listener: what the project's own code (a job, a subscriber) calls. It
	// carries no signed-in caller, so it may name the user it acts for.
	payments paymentpb.PaymentServiceClient
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func TestMain(m *testing.M) {
	var err error
	if api, err = device.New(http.DefaultClient, gateway, gatewayOrigin); err != nil {
		fmt.Fprintln(os.Stderr, "livetest:", err)
		os.Exit(1)
	}
	if db, err = sql.Open("postgres", databaseURL); err != nil {
		fmt.Fprintln(os.Stderr, "livetest: database:", err)
		os.Exit(1)
	}
	conn, err := grpc.NewClient(storageAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintln(os.Stderr, "livetest: storage:", err)
		os.Exit(1)
	}
	payments = paymentpb.NewPaymentServiceClient(conn)
	code := m.Run()
	_ = conn.Close()
	_ = db.Close()
	os.Exit(code)
}

// account is a signed-up user of the project.
type account struct {
	id    string
	token string
}

func signUp(t *testing.T) account {
	t.Helper()
	var resp struct {
		UserID string `json:"user_id"`
		Token  string `json:"token"`
	}
	// The sign-up proves a new device key: one per account.
	var session *device.Session
	const path = "/api/v1/auth/register"
	status, raw, err := exchange(http.MethodPost, path, map[string]any{
		"email":    "live-" + randomHex(6) + "@billing.example",
		"password": "e2eSecret!23",
	}, func(req *http.Request) (resp *http.Response, err error) {
		resp, session, err = api.Mint(req)
		return resp, err
	})
	if err := expect(http.MethodPost, path, status, raw, err, http.StatusOK, &resp); err != nil {
		t.Fatal(err)
	}
	api.Keep(resp.Token, session)
	return account{id: resp.UserID, token: resp.Token}
}

// The helpers that take a *testing.T fail the test with t.Fatal, which is
// only allowed on the test's own goroutine. Each has a try* twin that returns
// the error instead, for the tests that fire requests concurrently.

// call makes one REST call and decodes the answer into out, failing the test
// unless the status is want.
func call(t *testing.T, token, method, path string, body any, want int, out any) {
	t.Helper()
	if err := tryCall(token, method, path, body, want, out); err != nil {
		t.Fatal(err)
	}
}

func tryCall(token, method, path string, body any, want int, out any) error {
	status, raw, err := do(token, method, path, body)
	return expect(method, path, status, raw, err, want, out)
}

// expect checks an answer's status against want and decodes it into out.
func expect(method, path string, status int, raw []byte, err error, want int, out any) error {
	if err != nil {
		return err
	}
	if status != want {
		return fmt.Errorf("%s %s: status %d, want %d: %s", method, path, status, want, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s %s: decode %s: %v", method, path, raw, err)
		}
	}
	return nil
}

func do(token, method, path string, body any) (int, []byte, error) {
	return exchange(method, path, body, func(req *http.Request) (*http.Response, error) {
		return api.Do(req, token)
	})
}

// exchange builds one REST request, sends it with send and reads the answer.
func exchange(method, path string, body any, send func(*http.Request) (*http.Response, error)) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, gateway+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := send(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: read the answer: %v", method, path, err)
	}
	return resp.StatusCode, raw, nil
}

// payment is the part of the plugin's Payment the tests read. The status is
// the enum's number: w17 sends enums as integers.
type payment struct {
	ID                string `json:"id"`
	ProviderPaymentID string `json:"provider_payment_id"`
	Amount            string `json:"amount"`
	Status            int    `json:"status"`
}

const (
	statusRequiresAction = 1
	statusSucceeded      = 3
	statusFailed         = 4
	// statusAmountMismatch holds a payment the provider reports took
	// another amount than was asked.
	statusAmountMismatch = 7
)

func pay(t *testing.T, a account, amount string) payment {
	t.Helper()
	p, err := tryPay(a, amount)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func tryPay(a account, amount string) (payment, error) {
	var resp struct {
		Payment payment `json:"payment"`
	}
	err := tryCall(a.token, http.MethodPost, "/api/v1/billing/payments", map[string]any{
		"amount": amount, "currency": "eur", "idempotency_key": "order-" + randomHex(6),
	}, http.StatusOK, &resp)
	return resp.Payment, err
}

func paymentOf(t *testing.T, a account, id string) payment {
	t.Helper()
	var resp struct {
		Payment payment `json:"payment"`
	}
	call(t, a.token, http.MethodGet, "/api/v1/billing/payments/"+id, nil, http.StatusOK, &resp)
	return resp.Payment
}

// providerObject is what the fake provider holds for an id, as the real
// provider would put it into an event.
func providerObject(t *testing.T, id string) map[string]any {
	t.Helper()
	resp, err := http.Get(fakeStripe + "/_test/objects/" + id)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("provider has no object %s: status %d", id, resp.StatusCode)
	}
	var o map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&o); err != nil {
		t.Fatal(err)
	}
	return o
}

// event is a provider event about obj, as its webhook delivers it.
func event(typ string, obj map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{
		"id": "evt_" + randomHex(12), "object": "event", "type": typ,
		"created": time.Now().Unix(),
		"data":    map[string]any{"object": obj},
	})
	return b
}

// succeeded is the provider's payment_intent.succeeded for a payment intent:
// the intent as the fake holds it, succeeded, having taken its amount — a
// real event carries amount_received, and the plugin checks the success
// against it.
func succeeded(t *testing.T, providerPaymentID string) []byte {
	t.Helper()
	o := providerObject(t, providerPaymentID)
	o["status"] = "succeeded"
	o["amount_received"] = o["amount"]
	return event("payment_intent.succeeded", o)
}

// signature is the Stripe-Signature header for body, signed at ts with
// secret: HMAC-SHA256 over "<ts>.<body>".
func signature(body []byte, secret string, ts int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body)
	return "t=" + strconv.FormatInt(ts, 10) + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// deliver posts body to the webhook endpoint with the given signature
// header (none when empty) and returns the status.
func deliver(t *testing.T, body []byte, sig string) int {
	t.Helper()
	status, err := tryDeliver(body, sig)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func tryDeliver(body []byte, sig string) (int, error) {
	req, err := http.NewRequest(http.MethodPost, gateway+"/api/v1/webhooks/stripe", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if sig != "" {
		req.Header.Set("Stripe-Signature", sig)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("webhook delivery: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// deliverSigned delivers body signed now with the project's secret.
func deliverSigned(t *testing.T, body []byte) int {
	t.Helper()
	return deliver(t, body, signature(body, webhookSecret, time.Now().Unix()))
}

func tryDeliverSigned(body []byte) (int, error) {
	return tryDeliver(body, signature(body, webhookSecret, time.Now().Unix()))
}

func count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// eventID is the id of the event in body.
func eventID(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &e); err != nil || e.ID == "" {
		t.Fatalf("no event id in %s", body)
	}
	return e.ID
}
