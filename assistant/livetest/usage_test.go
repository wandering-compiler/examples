package livetest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentpb "github.com/wandering-compiler/examples/assistant/w17/stubs/pb/domains/assistant/agent"
)

// Each question writes one usage row: on the asker's scope, under the model
// the PROVIDER named (a dated id, not the one asked for), with the tokens it
// counted.
func TestAsk_RecordsOneRowWithWhatTheProviderCounted(t *testing.T) {
	a := signUp(t)
	mustAsk(t, a, "What should I plant?")
	eventually(t, "one usage row", func() (bool, string) {
		n := usageRows(t, a)
		return n == 1, fmt.Sprintf("%d rows", n)
	})
	var model string
	var measured bool
	var in, out int
	if err := db.QueryRowContext(context.Background(), `
		SELECT m.name, u.measured, u.input_tokens, u.output_tokens
		FROM agent_usage u JOIN agent_scope s ON s.id = u.scope_id JOIN agent_model m ON m.id = u.model_id
		WHERE s.external_id = $1`, a.id).Scan(&model, &measured, &in, &out); err != nil {
		t.Fatal(err)
	}
	// The stand-in counts 10 + the question's words in, 1 + the answer's out.
	if model != "gpt-test-2026-01-01" || !measured || in != 14 || out != 6 {
		t.Fatalf("row = model %q measured %v tokens %d/%d, want gpt-test-2026-01-01 true 14/6", model, measured, in, out)
	}
}

// A research run that searched once is two model calls (the call that asked
// for the tool, and the answer), so two rows.
func TestResearch_RecordsARowPerTurn(t *testing.T) {
	a := signUp(t)
	status, raw, err := do(a.token, http.MethodPost, "/api/v1/notes", map[string]any{"title": "Garden", "body": "Plant the tomatoes in May."})
	if err != nil || status != http.StatusOK {
		t.Fatalf("note: %d %s %v", status, raw, err)
	}
	status, raw, err = do(a.token, http.MethodPost, "/api/v1/assistant/research", map[string]any{"question": "tomatoes"})
	if err != nil || status != http.StatusOK {
		t.Fatalf("research: %d %s %v", status, raw, err)
	}
	eventually(t, "two usage rows", func() (bool, string) {
		n := usageRows(t, a)
		return n == 2, fmt.Sprintf("%d rows", n)
	})
}

// What the account's questions cost is reported once the rows land.
func TestMySpend_ReportsWhatWasRecorded(t *testing.T) {
	a := signUp(t)
	mustAsk(t, a, "One")
	mustAsk(t, a, "Two words")
	eventually(t, "spend of two calls", func() (bool, string) {
		status, raw, err := do(a.token, http.MethodGet, "/api/v1/assistant/spend", nil)
		if err != nil || status != http.StatusOK {
			return false, fmt.Sprintf("%d %s %v", status, raw, err)
		}
		var resp struct {
			Models []struct {
				Model        string `json:"model"`
				Calls        string `json:"calls"`
				InputTokens  string `json:"input_tokens"`
				OutputTokens string `json:"output_tokens"`
			} `json:"models"`
		}
		_ = json.Unmarshal(raw, &resp)
		ok := len(resp.Models) == 1 && resp.Models[0].Calls == "2" &&
			resp.Models[0].InputTokens == "23" && resp.Models[0].OutputTokens == "7"
		return ok, string(raw)
	})
}

// Many first questions at once from one account still make one scope: the
// scope is interned under a unique key, whatever the race.
func TestConcurrentFirstQuestions_OneScope(t *testing.T) {
	a := signUp(t)
	const n = 6
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			if status, raw, err := ask(a, fmt.Sprintf("Question %d", i)); err != nil || status != http.StatusOK {
				t.Errorf("ask %d: %d %s %v", i, status, raw, err)
			}
		})
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	eventually(t, "all usage rows", func() (bool, string) {
		got := usageRows(t, a)
		return got == n, fmt.Sprintf("%d rows", got)
	})
	if got := count(t, `SELECT count(*) FROM agent_scope WHERE external_id = $1`, a.id); got != 1 {
		t.Fatalf("scopes = %d, want 1", got)
	}
}

// A scope capped at nothing is refused before any call reaches the provider:
// a limit has to fail the request, not show up on the next invoice.
func TestLimit_RefusesACappedScope(t *testing.T) {
	a := signUp(t)
	ctx := context.Background()
	scope, err := usage.InternScope(ctx, &agentpb.InternScopeReq{ExternalId: a.id})
	if err != nil {
		t.Fatalf("InternScope: %v", err)
	}
	if _, err := usage.SetScopeLimit(ctx, &agentpb.SetScopeLimitReq{
		ScopeId: scope.GetId(), Window: agentpb.LimitWindow_LIMIT_WINDOW_TOTAL,
		LimitMinor: 0, Currency: "usd", ValidFrom: timestamppb.Now(),
	}); err != nil {
		t.Fatalf("SetScopeLimit: %v", err)
	}
	_, before := providerRequests(t)
	status, raw, err := ask(a, "Anything at all?")
	if err != nil || status != http.StatusTooManyRequests || !strings.Contains(string(raw), "RESOURCE_EXHAUSTED") {
		t.Fatalf("capped ask: %d %s %v, want 429 RESOURCE_EXHAUSTED", status, raw, err)
	}
	if _, after := providerRequests(t); after != before {
		t.Fatalf("the provider received %d requests for a refused question", after-before)
	}
}
