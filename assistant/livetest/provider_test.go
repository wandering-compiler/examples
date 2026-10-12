package livetest

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// canary is a prompt the provider quotes back when it refuses it. Neither the
// caller's answer nor any log may carry it: run.sh scans the stack's logs
// for it after the suite.
const canary = "canary-5e1f9c0b"

// providerRequests is every request the stand-in provider received. The
// client trusts the provider's authority through SSL_CERT_FILE, as the
// bundle does.
func providerRequests(t *testing.T) []map[string]any {
	t.Helper()
	resp, err := http.Get(fakeLLM + "/_test/requests")
	if err != nil {
		t.Fatalf("provider requests: %v", err)
	}
	defer resp.Body.Close()
	var out []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("provider requests: %v", err)
	}
	return out
}

// Every request the plugin sent asks the provider not to keep it, carries
// the project's instructions rather than anything a caller chose, and names
// the configured model.
func TestProvider_EveryRequestIsUnstoredAndUsesTheProjectsSettings(t *testing.T) {
	a := signUp(t)
	mustAsk(t, a, "Is anything kept?")
	reqs := providerRequests(t)
	if len(reqs) == 0 {
		t.Fatal("the provider received nothing")
	}
	for i, r := range reqs {
		if store, ok := r["store"].(bool); !ok || store {
			t.Errorf("request %d: store = %v, want false", i, r["store"])
		}
		if r["model"] != "gpt-test" {
			t.Errorf("request %d: model = %v, want the configured gpt-test", i, r["model"])
		}
		if ins, _ := r["instructions"].(string); !strings.HasPrefix(ins, "You answer questions from the person who keeps these notes.") {
			t.Errorf("request %d: instructions = %q", i, ins)
		}
	}
}

// A refusal that quotes the prompt reaches the caller as a refusal, without
// the quote.
func TestProvider_ARefusalDoesNotEchoThePrompt(t *testing.T) {
	a := signUp(t)
	status, raw, err := ask(a, "[error:400] "+canary)
	if err != nil {
		t.Fatal(err)
	}
	if status < 400 {
		t.Fatalf("a refused prompt answered %d: %s", status, raw)
	}
	if strings.Contains(string(raw), canary) {
		t.Fatalf("the answer carries the prompt the provider quoted: %s", raw)
	}
}

// A question stopped at the output budget still records what it spent.
func TestProvider_AnIncompleteAnswerIsStillRecorded(t *testing.T) {
	a := signUp(t)
	mustAsk(t, a, "[incomplete] Tell me everything")
	eventually(t, "one usage row", func() (bool, string) {
		n := usageRows(t, a)
		return n == 1, ""
	})
}
