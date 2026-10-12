// Package livetest proves, against the running stack, what the generated
// end-to-end suite cannot: what the agent plugin records in the database,
// what it sends the provider, and what it does under concurrent requests.
// run.sh brings the stack up with `w17ctl test --keep` and runs this package
// inside the stack's network, trusting the stand-in provider's certificate
// authority.
package livetest

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	agentpb "github.com/wandering-compiler/examples/assistant/w17/stubs/pb/domains/assistant/agent"
)

var (
	gateway     = env("GATEWAY_URL", "http://assistant-gateway:8080")
	fakeLLM     = env("FAKELLM_URL", "https://fakellm:8443")
	storageAddr = env("STORAGE_ADDR", "assistant-storage:50051")
	databaseURL = env("DATABASE_URL", "postgres://assistant:assistant@assistant-postgres:5432/assistant?sslmode=disable")

	db *sql.DB
	// usage is the agent plugin's usage mutations over the storage tier's
	// internal listener: what an operator's own tooling would call to set
	// limits.
	usage agentpb.UsageMutationClient
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func TestMain(m *testing.M) {
	var err error
	if db, err = sql.Open("postgres", databaseURL); err != nil {
		fmt.Fprintln(os.Stderr, "livetest: database:", err)
		os.Exit(1)
	}
	conn, err := grpc.NewClient(storageAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintln(os.Stderr, "livetest: storage:", err)
		os.Exit(1)
	}
	usage = agentpb.NewUsageMutationClient(conn)
	code := m.Run()
	_ = conn.Close()
	_ = db.Close()
	os.Exit(code)
}

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
	status, raw, err := do("", http.MethodPost, "/api/v1/auth/register", map[string]any{
		"email": "live-" + randomHex(6) + "@assistant.example", "password": "e2eSecret!23",
	})
	if err != nil || status != http.StatusOK || json.Unmarshal(raw, &resp) != nil {
		t.Fatalf("sign-up: %d %s %v", status, raw, err)
	}
	return account{id: resp.UserID, token: resp.Token}
}

// do makes one REST call. It never touches t, so it may run on any goroutine.
func do(token, method, path string, body any) (int, []byte, error) {
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
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

// ask puts one question; it never touches t.
func ask(a account, question string) (int, []byte, error) {
	return do(a.token, http.MethodPost, "/api/v1/assistant/ask", map[string]any{"question": question})
}

func mustAsk(t *testing.T, a account, question string) {
	t.Helper()
	status, raw, err := ask(a, question)
	if err != nil || status != http.StatusOK {
		t.Fatalf("ask %q: %d %s %v", question, status, raw, err)
	}
}

// eventually polls cond until it holds or the time is up. The plugin writes
// usage asynchronously, in batches, so a row lands a moment after the answer.
func eventually(t *testing.T, what string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		ok, state := cond()
		if ok {
			return
		}
		last = state
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s: not within 20s (last seen: %s)", what, last)
}

func count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// usageRows is how many usage rows the scope of an account has.
func usageRows(t *testing.T, a account) int {
	t.Helper()
	return count(t, `SELECT count(*) FROM agent_usage u JOIN agent_scope s ON s.id = u.scope_id WHERE s.external_id = $1`, a.id)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
