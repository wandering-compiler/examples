// Package device sends the live suite's REST calls the way the gateway in
// front of it wants them, with or without device keys.
//
// An auth method that binds every token to a device key (DPoP, RFC 9449)
// refuses a sign-up or sign-in that carries no proof of a key, and refuses a
// token presented as a Bearer. One that binds nothing ignores the proof. So
// the suite proves a key on every sign-up and sign-in ([Client.Mint]), and
// learns from the answer which kind of gateway it talks to: a binding
// gateway answers a verified mint with a DPoP-Nonce header, which no other
// gateway sends. The token of a bound session then travels as
// `Authorization: DPoP <token>` with a proof by the same key; any other token
// as `Authorization: Bearer <token>`, exactly as before.
//
// A proof names the URL it is for, and the gateway compares that with its
// configured public origin (`<P>_DPOP_ORIGIN`), never with the address the
// request reached it at. The suite reaches the gateway by its service name on
// the stack's network, which is not that origin; so proofs name the public
// origin given to [New] while the requests still go to the gateway's
// address.
//
// Each sign-up or sign-in makes a new key: one key per simulated user and
// session, as two people on two devices would have.
package device

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/wandering-compiler/sdk/go/lib/dpop"
	"github.com/wandering-compiler/sdk/go/lib/dpop/dpopclient"
)

// Client sends requests to one gateway. It is safe for concurrent use.
type Client struct {
	http   *http.Client
	target string // the origin the requests go to
	public string // the origin the proofs name

	sessions sync.Map // token → *Session, for the tokens bound to a key
}

// New returns a Client for the gateway at gatewayURL, which performs its
// requests with c (nil means http.DefaultClient). publicOrigin is the
// gateway's configured `<P>_DPOP_ORIGIN`, scheme://host[:port]; "" means the
// origin of gatewayURL.
func New(c *http.Client, gatewayURL, publicOrigin string) (*Client, error) {
	if c == nil {
		c = http.DefaultClient
	}
	u, err := url.Parse(gatewayURL)
	if err != nil {
		return nil, fmt.Errorf("device: gateway URL %q: %v", gatewayURL, err)
	}
	target, err := origin(u.Scheme + "://" + u.Host)
	if err != nil {
		return nil, fmt.Errorf("device: gateway URL %q: %v", gatewayURL, err)
	}
	public := target
	if publicOrigin != "" {
		p, err := url.Parse(publicOrigin)
		if err != nil || (p.Path != "" && p.Path != "/") || p.RawQuery != "" || p.Fragment != "" {
			return nil, fmt.Errorf("device: public origin %q: want scheme://host[:port] with no path", publicOrigin)
		}
		if public, err = origin(publicOrigin); err != nil {
			return nil, fmt.Errorf("device: public origin %q: %v", publicOrigin, err)
		}
	}
	return &Client{http: c, target: target, public: public}, nil
}

// origin normalises scheme://host[:port]: lower case, no default port, no
// trailing slash.
func origin(raw string) (string, error) {
	o, err := dpop.NormalizeHTU(raw)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(o, "/"), nil
}

// Session is one sign-up's or sign-in's device key, and whether the gateway
// bound the token it issued to it.
type Session struct {
	transport *dpopclient.Transport
	bound     bool
}

// Bound reports whether the gateway bound the session's token to its key.
func (s *Session) Bound() bool { return s.bound }

// Mint sends a sign-up or sign-in request with a proof of a new key and
// returns the answer and the session it opened. Hand the token from the
// answer to [Client.Keep].
func (c *Client) Mint(req *http.Request) (*http.Response, *Session, error) {
	key, err := dpop.GenerateKey(dpop.KeyP256)
	if err != nil {
		return nil, nil, err
	}
	base := c.http.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	if c.public != c.target {
		base = &redial{base: base, target: c.target}
	}
	t, err := dpopclient.NewTransport(base, key, tokenOf, c.public)
	if err != nil {
		return nil, nil, err
	}
	s := &Session{transport: t}
	resp, err := c.send(s, dpopclient.WithMint(req))
	if err != nil {
		return nil, nil, err
	}
	s.bound = resp.Header.Get(dpop.HeaderNonce) != ""
	return resp, s, nil
}

// Keep records the token a mint issued on s. A token of a bound session is
// then sent with proofs by its key; any other as a Bearer.
func (c *Client) Keep(token string, s *Session) {
	if token != "" && s != nil && s.bound {
		c.sessions.Store(token, s)
	}
}

// Do sends req on behalf of token: none for "", a DPoP-bound token with a
// proof by its session's key, any other as a Bearer.
func (c *Client) Do(req *http.Request, token string) (*http.Response, error) {
	if token == "" {
		return c.http.Do(req)
	}
	s, ok := c.sessions.Load(token)
	if !ok {
		req.Header.Set("Authorization", "Bearer "+token)
		return c.http.Do(req)
	}
	return c.send(s.(*Session), req.WithContext(context.WithValue(req.Context(), tokenKey{}, token)))
}

// send sends req through the session's transport, addressed to the public
// origin, which the proofs name; the transport below sends it to the
// gateway's own address.
func (c *Client) send(s *Session, req *http.Request) (*http.Response, error) {
	if c.public != c.target {
		req = req.Clone(req.Context())
		req.URL.Scheme, req.URL.Host, _ = strings.Cut(c.public, "://")
		req.Host = ""
	}
	hc := *c.http
	hc.Transport = s.transport
	return hc.Do(req)
}

type tokenKey struct{}

func tokenOf(ctx context.Context) (string, error) {
	tok, _ := ctx.Value(tokenKey{}).(string)
	return tok, nil
}

// redial sends a request addressed to the public origin to the gateway's own
// address.
type redial struct {
	base   http.RoundTripper
	target string
}

func (r *redial) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	out.URL.Scheme, out.URL.Host, _ = strings.Cut(r.target, "://")
	out.Host = ""
	return r.base.RoundTrip(out)
}
