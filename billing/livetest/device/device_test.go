package device

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/wandering-compiler/sdk/go/lib/dpop"
)

// publicOrigin is the binding gateway's configured origin: not the address
// the test server listens on, as the stack's gateway is not reached at its
// configured origin either.
const publicOrigin = "http://localhost:8080"

// bindingGateway verifies every proof with nonces on, as a gateway whose
// auth method binds tokens to device keys does: /register issues a token
// bound to the key of its proof and answers with a DPoP-Nonce; every other
// route wants that token as DPoP with a proof by the same key.
type bindingGateway struct {
	*httptest.Server
	v *dpop.Verifier

	mu       sync.Mutex
	bindings map[string]string // token → thumbprint
	bodies   []string
}

func newBindingGateway(t *testing.T) *bindingGateway {
	t.Helper()
	nonces, err := dpop.NewHMACNonces([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	g := &bindingGateway{bindings: map[string]string{}}
	if g.v, err = dpop.NewVerifier(dpop.Config{Origin: publicOrigin, Nonces: nonces}); err != nil {
		t.Fatal(err)
	}
	g.Server = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(g.Close)
	return g
}

func (g *bindingGateway) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	g.bodies = append(g.bodies, string(body))
	g.mu.Unlock()
	refuse := func(err error) {
		dpop.SetChallenge(w.Header(), err)
		w.WriteHeader(dpop.HTTPStatus(err))
		_, _ = io.WriteString(w, `{"code":"DEVICE_PROOF_REQUIRED"}`)
	}
	req := dpop.Request{Method: r.Method, Path: r.URL.EscapedPath(), Proofs: r.Header.Values(dpop.HeaderProof)}
	if r.URL.Path != "/register" {
		tok, ok := dpop.AccessToken(r.Header.Get("Authorization"))
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"code":"DEVICE_PROOF_REQUIRED"}`)
			return
		}
		g.mu.Lock()
		req.AccessToken, req.Binding = tok, g.bindings[tok]
		g.mu.Unlock()
	}
	tp, err := g.v.Verify(r.Context(), req)
	if err != nil {
		refuse(err)
		return
	}
	n, err := g.v.Nonce(tp)
	if err != nil {
		refuse(err)
		return
	}
	w.Header().Set(dpop.HeaderNonce, n)
	if r.URL.Path == "/register" {
		tok := "tok-" + tp
		g.mu.Lock()
		g.bindings[tok] = tp
		g.mu.Unlock()
		_, _ = io.WriteString(w, tok)
		return
	}
	_, _ = io.WriteString(w, "ok:"+tp)
}

// plainGateway binds nothing, as a released auth plugin: it ignores the
// proof on /register and wants every token as a Bearer, with no proof.
type plainGateway struct {
	*httptest.Server
	mu    sync.Mutex
	seen  []http.Header
	count int
}

func newPlainGateway(t *testing.T) *plainGateway {
	t.Helper()
	g := &plainGateway{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.seen = append(g.seen, r.Header.Clone())
		g.count++
		n := g.count
		g.mu.Unlock()
		if r.URL.Path == "/register" {
			_, _ = io.WriteString(w, "plain-"+string(rune('a'+n)))
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer plain-") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(g.Close)
	return g
}

func request(t *testing.T, method, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func read(t *testing.T, resp *http.Response) (int, string) {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

// signUp mints a session on c and keeps its token.
func signUp(t *testing.T, c *Client, gateway string) (string, *Session) {
	t.Helper()
	resp, s, err := c.Mint(request(t, http.MethodPost, gateway+"/register", `{"email":"a@b.c"}`))
	if err != nil {
		t.Fatal(err)
	}
	status, tok := read(t, resp)
	if status != http.StatusOK {
		t.Fatalf("sign-up: %d %s", status, tok)
	}
	c.Keep(tok, s)
	return tok, s
}

// Against a binding gateway reached at another address than its configured
// origin: the sign-up proves a key (after the nonce round trip, its body
// sent twice), the session is bound, and the token travels as DPoP with
// proofs by that key — one key per sign-up, concurrent requests included.
func TestBindingGateway(t *testing.T) {
	g := newBindingGateway(t)
	c, err := New(nil, g.URL, publicOrigin)
	if err != nil {
		t.Fatal(err)
	}
	alice, as := signUp(t, c, g.URL)
	bob, bs := signUp(t, c, g.URL)
	if !as.Bound() || !bs.Bound() {
		t.Fatalf("bound = %v, %v; want both bound", as.Bound(), bs.Bound())
	}
	if alice == bob {
		t.Fatalf("two sign-ups share the key of %s", alice)
	}
	g.mu.Lock()
	if g.bodies[0] != `{"email":"a@b.c"}` || g.bodies[1] != g.bodies[0] {
		t.Errorf("sign-up bodies = %q, want the body on the refused attempt and on its retry", g.bodies[:2])
	}
	g.mu.Unlock()

	var wg sync.WaitGroup
	errs := make(chan string, 20)
	for i := 0; i < 20; i++ {
		tok := alice
		if i%2 == 1 {
			tok = bob
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, g.URL+"/things", strings.NewReader("x"))
			resp, err := c.Do(req, tok)
			if err != nil {
				errs <- err.Error()
				return
			}
			defer func() { _ = resp.Body.Close() }()
			b, _ := io.ReadAll(resp.Body)
			if want := "ok:" + strings.TrimPrefix(tok, "tok-"); resp.StatusCode != http.StatusOK || string(b) != want {
				errs <- resp.Status + " " + string(b) + ", want " + want
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}

	// An unknown token is a Bearer, which this gateway refuses.
	resp, err := c.Do(request(t, http.MethodGet, g.URL+"/things", ""), "tok-unknown")
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := read(t, resp); status != http.StatusUnauthorized {
		t.Errorf("a Bearer token: %d, want 401", status)
	}
}

// A proof naming the address the gateway was reached at, not its configured
// origin, is refused: the public origin is what makes the sign-up work.
func TestBindingGateway_WrongOriginRefused(t *testing.T) {
	g := newBindingGateway(t)
	c, err := New(nil, g.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	resp, _, err := c.Mint(request(t, http.MethodPost, g.URL+"/register", "{}"))
	if err != nil {
		t.Fatal(err)
	}
	if status, body := read(t, resp); status != http.StatusUnauthorized {
		t.Fatalf("sign-up signed for %s: %d %s, want 401", g.URL, status, body)
	}
}

// Against a gateway that binds nothing: the sign-up still carries a proof,
// which it ignores; the session is not bound and the token travels as a
// Bearer with no proof, as before.
func TestPlainGateway(t *testing.T) {
	g := newPlainGateway(t)
	c, err := New(&http.Client{}, g.URL+"/", publicOrigin)
	if err != nil {
		t.Fatal(err)
	}
	tok, s := signUp(t, c, g.URL)
	if s.Bound() {
		t.Fatal("a session on a gateway that sent no DPoP-Nonce is bound")
	}
	resp, err := c.Do(request(t, http.MethodGet, g.URL+"/things", ""), tok)
	if err != nil {
		t.Fatal(err)
	}
	if status, body := read(t, resp); status != http.StatusOK || body != "ok" {
		t.Fatalf("request with the token: %d %s", status, body)
	}
	resp, err = c.Do(request(t, http.MethodGet, g.URL+"/public", ""), "")
	if err != nil {
		t.Fatal(err)
	}
	read(t, resp)
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.seen) != 3 {
		t.Fatalf("gateway saw %d requests, want 3", len(g.seen))
	}
	if g.seen[0].Get(dpop.HeaderProof) == "" {
		t.Error("the sign-up carried no proof")
	}
	if got := g.seen[1].Get("Authorization"); got != "Bearer "+tok || g.seen[1].Get(dpop.HeaderProof) != "" {
		t.Errorf("request with the token: Authorization %q, DPoP %q; want the Bearer alone", got, g.seen[1].Get(dpop.HeaderProof))
	}
	if g.seen[2].Get("Authorization") != "" || g.seen[2].Get(dpop.HeaderProof) != "" {
		t.Error("a request without a token carried credentials")
	}
}

func TestNew_RefusesAnOriginWithAPath(t *testing.T) {
	if _, err := New(nil, "http://gateway:8080", "https://example.com/api"); err == nil {
		t.Fatal("a public origin with a path was accepted")
	}
}
