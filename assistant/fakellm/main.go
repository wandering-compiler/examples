// fakellm is a small stand-in for the language-model provider the agent
// plugin calls (the Responses API, as an Azure OpenAI deployment serves it),
// for running this example without a provider account.
//
// It serves HTTPS only. The plugin refuses to send its API key over plain
// HTTP to anything but loopback, and an example should not need a switch that
// a production deployment could leave on. So at start the fake makes its own
// certificate authority and a certificate for its hostnames, and writes the
// authority's certificate where the stack can trust it (FAKELLM_CA_OUT).
//
// Answers are deterministic, and a marker in the question selects the less
// ordinary ones:
//
//	[incomplete]   the answer stops at the output budget (status incomplete)
//	[error:429]    the call fails with that HTTP status (a 400 quotes the
//	               prompt back, as a provider's refusal does)
//
// When the request offers tools and carries no tool output yet, the model
// "decides" to call the first tool with {"query": <the question>}; once tool
// output is in the input, it answers from it.
//
// GET /_test/requests returns what the fake received (newest last), so a test
// can check what the plugin sent; the API key is never recorded.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	addr := env("FAKELLM_ADDR", ":8443")
	apiKey := env("FAKELLM_API_KEY", "test-key")
	hosts := strings.Split(env("FAKELLM_HOSTS", "fakellm,localhost,127.0.0.1"), ",")
	caOut := env("FAKELLM_CA_OUT", "/ca/ca.pem")

	cert, caPEM, err := selfSigned(hosts)
	if err != nil {
		log.Fatalf("fakellm: certificates: %v", err)
	}
	if err := os.WriteFile(caOut, caPEM, 0o644); err != nil {
		log.Fatalf("fakellm: write %s: %v", caOut, err)
	}

	f := &fake{apiKey: apiKey}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /_test/requests", f.listRequests)
	mux.HandleFunc("POST /", f.respond)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	log.Printf("fakellm listening on %s (TLS), CA at %s", addr, caOut)
	log.Fatal(srv.ListenAndServeTLS("", ""))
}

// selfSigned makes a certificate authority and a server certificate it signs
// for hosts. Only the authority's certificate leaves the process.
func selfSigned(hosts []string) (tls.Certificate, []byte, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	now := time.Now()
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fakellm test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(7 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	ca, _ := x509.ParseCertificate(caDER)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: hosts[0]},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(7 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), nil
}

type fake struct {
	apiKey string
	mu     sync.Mutex
	seq    int
	seen   []json.RawMessage
}

func (f *fake) listRequests(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(f.seen)
}

// request is the part of a Responses API request the fake reads.
type request struct {
	Model           string                  `json:"model"`
	Instructions    string                  `json:"instructions"`
	Input           []json.RawMessage       `json:"input"`
	Tools           []struct{ Name string } `json:"tools"`
	Stream          bool                    `json:"stream"`
	MaxOutputTokens int                     `json:"max_output_tokens"`
}

var errorMarker = regexp.MustCompile(`\[error:(\d{3})\]`)

func (f *fake) respond(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/responses") {
		http.NotFound(w, r)
		return
	}
	key := r.Header.Get("Api-Key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if key != f.apiKey {
		apiError(w, http.StatusUnauthorized, "invalid_api_key", "Access denied due to invalid subscription key.")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		apiError(w, http.StatusBadRequest, "invalid_request", "unreadable body")
		return
	}
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		apiError(w, http.StatusBadRequest, "invalid_request", "malformed JSON")
		return
	}
	f.mu.Lock()
	f.seq++
	id := f.seq
	f.seen = append(f.seen, raw)
	if len(f.seen) > 200 {
		f.seen = f.seen[len(f.seen)-200:]
	}
	f.mu.Unlock()

	question, toolOutput, hasToolOutput := readInput(req.Input)
	if m := errorMarker.FindStringSubmatch(question); m != nil {
		code, _ := strconv.Atoi(m[1])
		if code == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "0")
		}
		msg := "injected failure"
		if code == http.StatusBadRequest {
			// A real provider quotes what it refused; the plugin must not
			// pass that on, to the caller or to its logs.
			msg = "Invalid prompt: '" + question + "'"
		}
		apiError(w, code, "injected", msg)
		return
	}

	resp := map[string]any{
		"id": fmt.Sprintf("resp_%d", id), "object": "response", "created_at": time.Now().Unix(),
		"model": req.Model + "-2026-01-01", "status": "completed",
	}
	var text string
	var output []any
	switch {
	case len(req.Tools) > 0 && !hasToolOutput:
		args, _ := json.Marshal(map[string]string{"query": queryOf(question)})
		output = []any{map[string]any{
			"type": "function_call", "id": fmt.Sprintf("fc_%d", id), "call_id": fmt.Sprintf("call_%d", id),
			"name": req.Tools[0].Name, "arguments": string(args), "status": "completed",
		}}
	case hasToolOutput:
		text = "From your notes: " + toolOutput
	default:
		text = "Answer: " + question
	}
	if strings.Contains(question, "[incomplete]") {
		resp["status"] = "incomplete"
		resp["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
		text = firstWords(text, 2)
	}
	if text != "" {
		output = []any{map[string]any{
			"type": "message", "id": fmt.Sprintf("msg_%d", id), "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
		}}
	}
	resp["output"] = output
	in, out := 10+words(question)+words(toolOutput), 1+words(text)
	resp["usage"] = map[string]any{
		"input_tokens": in, "output_tokens": out, "total_tokens": in + out,
		"input_tokens_details":  map[string]any{"cached_tokens": 0},
		"output_tokens_details": map[string]any{"reasoning_tokens": 0},
	}

	if !req.Stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	seq := 0
	send := func(ev map[string]any) {
		seq++
		ev["sequence_number"] = seq
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev["type"], b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	send(map[string]any{"type": "response.created", "response": map[string]any{"id": resp["id"], "status": "in_progress", "model": resp["model"]}})
	for _, word := range strings.SplitAfter(text, " ") {
		if word == "" {
			continue
		}
		send(map[string]any{"type": "response.output_text.delta", "item_id": fmt.Sprintf("msg_%d", id), "output_index": 0, "content_index": 0, "delta": word, "logprobs": []any{}})
	}
	terminal := "response.completed"
	if resp["status"] == "incomplete" {
		terminal = "response.incomplete"
	}
	send(map[string]any{"type": terminal, "response": resp})
}

// readInput returns the last user message's text, and the tool output when
// the input carries one.
func readInput(items []json.RawMessage) (question, toolOutput string, hasToolOutput bool) {
	for _, raw := range items {
		var it struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			Output  string          `json:"output"`
		}
		if json.Unmarshal(raw, &it) != nil {
			continue
		}
		switch {
		case it.Type == "function_call_output":
			toolOutput, hasToolOutput = it.Output, true
		case it.Role == "user":
			question = contentText(it.Content)
		}
	}
	return question, toolOutput, hasToolOutput
}

func contentText(c json.RawMessage) string {
	var s string
	if json.Unmarshal(c, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(c, &parts)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

var marker = regexp.MustCompile(`\[[a-z]+(:[^\]]*)?\]`)

// queryOf is what the model searches for: the question without markers.
func queryOf(question string) string {
	return strings.TrimSpace(marker.ReplaceAllString(question, ""))
}

func words(s string) int { return len(strings.Fields(s)) }

func firstWords(s string, n int) string {
	f := strings.Fields(s)
	if len(f) > n {
		f = f[:n]
	}
	return strings.Join(f, " ")
}

func apiError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": msg, "type": "invalid_request_error"}})
}
