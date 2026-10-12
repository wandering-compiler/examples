// fakestripe is a small, stateful stand-in for the part of the Stripe API the
// payment plugin calls, for running this example without a Stripe account.
//
// Stripe's own mock (stripe/stripe-mock) answers every call with canned
// values: every payment intent it creates has the same id, so a second
// payment collides with the first on the plugin's unique provider id, and
// it enforces neither idempotency keys nor the refund cap. This fake keeps
// what it creates in memory and enforces both, the way the real API does.
//
// It sends no webhooks: the live suite (../livetest) signs and posts them
// itself, so a test decides when a payment settles and can deliver an event
// twice, late or forged.
//
// It is a test double, not a Stripe implementation: only the endpoints and
// fields the plugin's Stripe driver uses exist.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// rejectedKey is an API key the fake refuses as invalid, so a test can see
// what a misconfigured key does to the plugin's answers. The message echoes
// the key the way Stripe's does; the plugin must not pass it on.
const rejectedKey = "sk_test_rejected"

type object = map[string]any

type store struct {
	mu   sync.Mutex
	seq  int
	objs map[string]map[string]object // kind -> id -> object
	// order keeps creation order per kind, for lists.
	order map[string][]string
	// idem maps an Idempotency-Key to the request it first came with and
	// the answer it got, replayed for a retry with the same parameters.
	idem map[string]idemEntry
}

type idemEntry struct {
	path   string
	params string
	status int
	body   []byte
}

func newStore() *store {
	return &store{objs: map[string]map[string]object{}, order: map[string][]string{}, idem: map[string]idemEntry{}}
}

func (s *store) newID(prefix string) string {
	s.seq++
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s_%s%04d", prefix, hex.EncodeToString(b), s.seq)
}

func (s *store) put(kind string, o object) {
	if s.objs[kind] == nil {
		s.objs[kind] = map[string]object{}
	}
	id := o["id"].(string)
	if _, ok := s.objs[kind][id]; !ok {
		s.order[kind] = append(s.order[kind], id)
	}
	s.objs[kind][id] = o
}

func main() {
	addr := os.Getenv("FAKESTRIPE_ADDR")
	if addr == "" {
		addr = ":12111"
	}
	s := newStore()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("POST /v1/customers", s.handle(s.createCustomer))
	mux.HandleFunc("POST /v1/payment_intents", s.handle(s.createPaymentIntent))
	mux.HandleFunc("POST /v1/refunds", s.handle(s.createRefund))
	mux.HandleFunc("GET /v1/refunds", s.handle(s.listRefunds))
	mux.HandleFunc("POST /v1/prices", s.handle(s.createPrice))
	mux.HandleFunc("POST /v1/subscriptions", s.handle(s.createSubscription))
	mux.HandleFunc("GET /v1/subscriptions", s.handle(s.listSubscriptions))
	// Test-only: what the fake holds for one object, so a test can check
	// what the plugin sent (metadata, amounts) without trusting the plugin.
	mux.HandleFunc("GET /_test/objects/{id}", s.getObject)
	log.Printf("fakestripe listening on %s", addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

type apiFunc func(form url.Values) (int, object)

// handle authenticates the call, applies the idempotency key and serialises
// every request under one lock. Unlike Stripe, which answers 409 to a request
// whose key is still in flight on another, a concurrent retry here waits and
// gets the first request's answer: a test cannot rely on this fake to show
// how the plugin copes with that 409.
func (s *store) handle(fn apiFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch {
		case !ok || !strings.HasPrefix(key, "sk_test_"):
			writeErr(w, http.StatusUnauthorized, "invalid_request_error", "", "You did not provide an API key.")
			return
		case key == rejectedKey:
			writeErr(w, http.StatusUnauthorized, "invalid_request_error", "api_key_invalid", "Invalid API Key provided: "+key)
			return
		}
		if err := r.ParseForm(); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request_error", "", "malformed form body")
			return
		}
		form := r.Form

		s.mu.Lock()
		defer s.mu.Unlock()

		idemKey := r.Header.Get("Idempotency-Key")
		params := canonical(r.URL.Path, form)
		if idemKey != "" {
			if prev, seen := s.idem[idemKey]; seen {
				if prev.path != r.URL.Path || prev.params != params {
					writeErr(w, http.StatusBadRequest, "idempotency_error", "",
						"Keys for idempotent requests can only be used with the same parameters they were first used with.")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Idempotent-Replayed", "true")
				w.WriteHeader(prev.status)
				_, _ = w.Write(prev.body)
				return
			}
		}
		status, body := fn(form)
		raw, _ := json.Marshal(body)
		// Stripe saves the answer to a keyed request only once the endpoint
		// has started executing: a request that failed validation saves
		// nothing, and a retry with the key runs again. Every error this
		// fake answers is a validation error, so only a success is saved.
		if idemKey != "" && status < 400 {
			s.idem[idemKey] = idemEntry{path: r.URL.Path, params: params, status: status, body: raw}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(raw)
	}
}

func (s *store) getObject(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	for _, objs := range s.objs {
		if o, ok := objs[id]; ok {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(o)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *store) createCustomer(form url.Values) (int, object) {
	o := object{
		"id": s.newID("cus"), "object": "customer",
		"email": form.Get("email"), "metadata": metadata(form),
	}
	s.put("customer", o)
	return http.StatusOK, o
}

func (s *store) createPaymentIntent(form url.Values) (int, object) {
	amount, err := strconv.ParseInt(form.Get("amount"), 10, 64)
	if err != nil || amount <= 0 {
		return apiErr(http.StatusBadRequest, "invalid_request_error", "parameter_invalid_integer", "Invalid positive integer: amount")
	}
	if form.Get("currency") == "" {
		return apiErr(http.StatusBadRequest, "invalid_request_error", "parameter_missing", "Missing required param: currency.")
	}
	if c := form.Get("customer"); c != "" && s.objs["customer"][c] == nil {
		return apiErr(http.StatusBadRequest, "invalid_request_error", "resource_missing", "No such customer: '"+c+"'")
	}
	id := s.newID("pi")
	o := object{
		"id": id, "object": "payment_intent",
		"amount": amount, "currency": form.Get("currency"),
		"customer": form.Get("customer"), "description": form.Get("description"),
		"status": "requires_payment_method", "client_secret": id + "_secret_" + s.newID("cs"),
		"metadata": metadata(form), "created": time.Now().Unix(),
	}
	s.put("payment_intent", o)
	return http.StatusOK, o
}

func (s *store) createRefund(form url.Values) (int, object) {
	piID := form.Get("payment_intent")
	pi := s.objs["payment_intent"][piID]
	if pi == nil {
		return apiErr(http.StatusBadRequest, "invalid_request_error", "resource_missing", "No such payment_intent: '"+piID+"'")
	}
	captured := pi["amount"].(int64)
	var refunded int64
	for _, id := range s.order["refund"] {
		if r := s.objs["refund"][id]; r["payment_intent"] == piID {
			refunded += r["amount"].(int64)
		}
	}
	amount := captured - refunded
	if a := form.Get("amount"); a != "" {
		n, err := strconv.ParseInt(a, 10, 64)
		if err != nil || n <= 0 {
			return apiErr(http.StatusBadRequest, "invalid_request_error", "parameter_invalid_integer", "Invalid positive integer: amount")
		}
		amount = n
	}
	if amount <= 0 {
		return apiErr(http.StatusBadRequest, "invalid_request_error", "charge_already_refunded", "Charge has already been refunded.")
	}
	if refunded+amount > captured {
		return apiErr(http.StatusBadRequest, "invalid_request_error", "amount_too_large",
			fmt.Sprintf("Refund amount (%d) is greater than unrefunded amount on charge (%d)", amount, captured-refunded))
	}
	o := object{
		"id": s.newID("re"), "object": "refund", "payment_intent": piID,
		"amount": amount, "currency": pi["currency"], "status": "succeeded",
		"metadata": metadata(form), "created": time.Now().Unix(),
	}
	s.put("refund", o)
	return http.StatusOK, o
}

func (s *store) listRefunds(form url.Values) (int, object) {
	pi := form.Get("payment_intent")
	return s.list("refund", form, func(o object) bool { return pi == "" || o["payment_intent"] == pi })
}

func (s *store) createPrice(form url.Values) (int, object) {
	amount, err := strconv.ParseInt(form.Get("unit_amount"), 10, 64)
	if err != nil || amount <= 0 {
		return apiErr(http.StatusBadRequest, "invalid_request_error", "parameter_invalid_integer", "Invalid positive integer: unit_amount")
	}
	o := object{
		"id": s.newID("price"), "object": "price", "unit_amount": amount,
		"currency":  form.Get("currency"),
		"recurring": object{"interval": form.Get("recurring[interval]")},
		"product":   s.newID("prod"),
	}
	s.put("price", o)
	return http.StatusOK, o
}

func (s *store) createSubscription(form url.Values) (int, object) {
	cus, price := form.Get("customer"), form.Get("items[0][price]")
	if s.objs["customer"][cus] == nil {
		return apiErr(http.StatusBadRequest, "invalid_request_error", "resource_missing", "No such customer: '"+cus+"'")
	}
	if s.objs["price"][price] == nil {
		return apiErr(http.StatusBadRequest, "invalid_request_error", "resource_missing", "No such price: '"+price+"'")
	}
	now := time.Now()
	o := object{
		"id": s.newID("sub"), "object": "subscription", "customer": cus,
		"status": "active", "current_period_end": now.AddDate(0, 1, 0).Unix(),
		"items":    object{"object": "list", "data": []object{{"price": object{"id": price}}}},
		"metadata": metadata(form), "created": now.Unix(),
	}
	s.put("subscription", o)
	return http.StatusOK, o
}

func (s *store) listSubscriptions(form url.Values) (int, object) {
	cus := form.Get("customer")
	return s.list("subscription", form, func(o object) bool { return cus == "" || o["customer"] == cus })
}

// list pages through one kind, newest first, honouring limit and
// starting_after the way Stripe's list endpoints do.
func (s *store) list(kind string, form url.Values, keep func(object) bool) (int, object) {
	limit := 10
	if n, err := strconv.Atoi(form.Get("limit")); err == nil && n > 0 && n <= 100 {
		limit = n
	}
	ids := s.order[kind]
	var all []object
	for i := len(ids) - 1; i >= 0; i-- {
		if o := s.objs[kind][ids[i]]; keep(o) {
			all = append(all, o)
		}
	}
	if after := form.Get("starting_after"); after != "" {
		found := false
		for i, o := range all {
			if o["id"] == after {
				all, found = all[i+1:], true
				break
			}
		}
		// Stripe refuses a cursor it does not know; starting over from the
		// first page would hide a paging bug in the caller.
		if !found {
			return apiErr(http.StatusBadRequest, "invalid_request_error", "resource_missing", "No such object: '"+after+"'")
		}
	}
	more := len(all) > limit
	if more {
		all = all[:limit]
	}
	if all == nil {
		all = []object{}
	}
	return http.StatusOK, object{"object": "list", "data": all, "has_more": more}
}

// metadata collects metadata[k]=v form fields.
func metadata(form url.Values) map[string]string {
	m := map[string]string{}
	for k, v := range form {
		if inner, ok := strings.CutPrefix(k, "metadata["); ok && strings.HasSuffix(inner, "]") && len(v) > 0 {
			m[strings.TrimSuffix(inner, "]")] = v[0]
		}
	}
	return m
}

// canonical is a stable digest of a request's parameters, to tell a retry
// from a different request reusing a key.
func canonical(path string, form url.Values) string {
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	h.Write([]byte(path))
	for _, k := range keys {
		fmt.Fprintf(h, "\x00%s=%s", k, strings.Join(form[k], "\x01"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func apiErr(status int, typ, code, msg string) (int, object) {
	e := object{"type": typ, "message": msg}
	if code != "" {
		e["code"] = code
	}
	return status, object{"error": e}
}

func writeErr(w http.ResponseWriter, status int, typ, code, msg string) {
	_, body := apiErr(status, typ, code, msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
