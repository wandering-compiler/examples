package livetest

// A server stream opened by a POST (rest.proto: `method: POST` on
// tasks.TasksService.AskAboutTasks). A chat turn's question and history are a
// JSON body: in a URL they would land in every proxy's access log, and the
// history does not fit a query string at all.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// sseData reads a text/event-stream to its end and returns the data lines, or
// fails on an error frame.
func sseData(t *testing.T, resp *http.Response) []string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	var out []string
	errored := false
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: error"):
			errored = true
		case strings.HasPrefix(line, "data:"):
			if errored {
				t.Fatalf("the stream answered with an error frame: %s", line)
			}
			out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return out
}

// The question and the history reach the handler from the body, whole, and
// the answer streams back as server-sent events.
func TestStreamOverPost_TheBodyReachesTheHandler(t *testing.T) {
	token := signUp(t)
	question := "which of my tasks are due " + nonce() + "?"
	body, _ := json.Marshal(map[string]any{
		"question": question,
		"history":  []string{"what is on my list?", "and the overdue ones?", "thanks"},
	})
	req, err := http.NewRequest(http.MethodPost, gateway+"/api/v1/tasks/ask", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := api.Do(req, token)
	if err != nil {
		t.Fatalf("open the stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("POST /tasks/ask answered %d %q, want 200 text/event-stream", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	frames := sseData(t, resp)
	var texts []string
	for _, f := range frames {
		var chunk struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(f), &chunk); err != nil {
			t.Fatalf("frame %q is not a chunk: %v", f, err)
		}
		texts = append(texts, chunk.Text)
	}
	want := []string{"heard: " + question, "after 3 earlier turn(s)"}
	if strings.Join(texts, "\n") != strings.Join(want, "\n") {
		t.Errorf("frames %q, want %q — the body did not reach the handler whole", texts, want)
	}
}

// The same stream asked by GET, the question in the URL, is not served: the
// route is the POST, so nothing of a turn can be put in a URL by mistake.
func TestStreamOverPost_AQuestionInTheURLIsNotServed(t *testing.T) {
	token := signUp(t)
	req, err := http.NewRequest(http.MethodGet, gateway+"/api/v1/tasks/ask?question="+url.QueryEscape("in the url "+nonce()), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := api.Do(req, token)
	if err != nil {
		t.Fatalf("GET /tasks/ask: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Errorf("GET /tasks/ask?question=… answered 200: the question rode the URL")
	}
}
