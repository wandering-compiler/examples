// Package livetest proves, against the running stack, what the generated
// end-to-end suite cannot: that the one app-server binary, with all five
// components of the app domain folded into it, really serves each of them.
//
// The admin answers on its own listener inside the process; the REST
// surface reaches the storage tier; the business facade runs; an event is
// delivered exactly once, so no second subscriber runs beside the binary;
// a facade's event is published; the business tier reads a file the
// gateway wrote, stores a file it made, and answers one as the body. run.sh
// brings the stack up with `w17ctl test --keep` and runs this package inside
// the stack's network.
package livetest

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Both targets are compose-network addresses (service name + container
// port); the stack publishes nothing the suite needs.
var (
	restTarget  = flag.String("rest", envOr("E2E_REST", "http://app-server:8080"), "base URL of the binary's REST transport, on the compose network")
	adminTarget = flag.String("admin", envOr("E2E_ADMIN", "http://app-server:9090"), "base URL of the binary's admin transport, on the compose network")
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// TestComposed_AdminSurfaceAnswersInProcess: the folded admin keeps its
// own listener, and serves its handler chain from inside the binary.
func TestComposed_AdminSurfaceAnswersInProcess(t *testing.T) {
	// The SPA shell is served unauthenticated (it has to be — it carries
	// the login form), so a 200 here proves the admin's own handler chain
	// is bound and reachable inside the composed process.
	code, body, ctype := get(t, *adminTarget+"/admin/")
	if code != http.StatusOK {
		t.Errorf("GET /admin/ on the published admin port: got %d, want 200\nbody: %s", code, trunc(body))
	}
	if !strings.Contains(ctype, "text/html") {
		t.Errorf("GET /admin/ content-type = %q, want text/html (the embedded SPA shell)", ctype)
	}

	// And the API behind it answers as the admin, fail-closed. This
	// distinguishes "the admin is really here" from "something else
	// happens to answer on this port", and pins that the surface is not
	// accidentally unauthenticated.
	code, body, _ = get(t, *adminTarget+"/admin/api/list/NoteAdmin")
	if code != http.StatusUnauthorized {
		t.Errorf("unauthenticated GET /admin/api/list/NoteAdmin: got %d, want 401\nbody: %s", code, trunc(body))
	}
}

// TestComposed_RestSurfaceServesTheFoldedTiers: the binary booted with a
// working storage tier (its connection configured in the binary's own env).
func TestComposed_RestSurfaceServesTheFoldedTiers(t *testing.T) {
	id := createNote(t, "packaging proof")

	code, body, _ := get(t, fmt.Sprintf("%s/api/v1/notes/%d", *restTarget, id))
	if code != http.StatusOK {
		t.Fatalf("GET /notes/%d: got %d, want 200\nbody: %s", id, code, trunc(body))
	}
	var note map[string]any
	if err := json.Unmarshal(body, &note); err != nil {
		t.Fatalf("decode note: %v\nbody: %s", err, trunc(body))
	}
	if got := note["title"]; got != "packaging proof" {
		t.Errorf("note title = %v, want %q", got, "packaging proof")
	}
}

// TestComposed_FacadeRunsInProcess drives the business tier through the
// same binary. The facade tags the title, so the prefix is proof the
// request reached the folded business tier and came back.
func TestComposed_FacadeRunsInProcess(t *testing.T) {
	code, body, _ := post(t, *restTarget+"/api/v1/notes/checked", map[string]any{
		"owner_id": ownerID,
		"title":    "via facade",
		"body":     "b",
	})
	if code != http.StatusOK {
		t.Fatalf("POST /notes/checked: got %d, want 200\nbody: %s", code, trunc(body))
	}
	var note map[string]any
	if err := json.Unmarshal(body, &note); err != nil {
		t.Fatalf("decode note: %v\nbody: %s", err, trunc(body))
	}
	if got, _ := note["title"].(string); got != "[checked] via facade" {
		t.Errorf("facade title = %q, want %q", got, "[checked] via facade")
	}
}

// TestComposed_EventIsDeliveredExactlyOnce: one subscriber consumes the
// stream. A standalone eventbus container beside the binary would be a
// second subscriber on the same durable prefix, and one CreateNote would
// yield two NoteDelivery rows; the handler is deliberately non-idempotent
// so that shows up as a count.
//
// A duplicate arrives at its own pace, so the test waits after the first
// delivery before it counts.
func TestComposed_EventIsDeliveredExactlyOnce(t *testing.T) {
	id := createNote(t, "delivery proof")

	deadline := time.Now().Add(90 * time.Second)
	for {
		if deliveries(t, id) >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("NoteCreated was never delivered for note %d — the absorbed eventbus tier is not consuming", id)
		}
		time.Sleep(time.Second)
	}

	// Let a second delivery arrive if one is coming.
	time.Sleep(10 * time.Second)

	if n := deliveries(t, id); n != 1 {
		t.Errorf("NoteCreated delivered %d times for note %d, want exactly 1.\n"+
			"More than one means a standalone <domain>-eventbus container is running ALONGSIDE the\n"+
			"composed binary that already absorbed it — two subscribers on one durable prefix.", n, id)
	}
}

// TestComposed_FacadeEmitReachesTheBus: a BUSINESS-tier
// `(w17.event_emit)` is published. The emit wrapper is installed by the
// binary that serves the facade; without it the facade still answers and
// nothing is published.
//
// The observable is the title stamp rather than a delivery count: the
// storage tier's NoteCreated fires for this note too (the facade delegates
// to CreateNote), so a count cannot separate the two publishers. Only the
// NoteChecked handler writes the stamp.
func TestComposed_FacadeEmitReachesTheBus(t *testing.T) {
	code, body, _ := post(t, *restTarget+"/api/v1/notes/checked", map[string]any{
		"owner_id": ownerID,
		"title":    "facade emit proof",
		"body":     "b",
	})
	if code != http.StatusOK {
		t.Fatalf("POST /notes/checked: got %d, want 200\nbody: %s", code, trunc(body))
	}
	var note map[string]any
	if err := json.Unmarshal(body, &note); err != nil {
		t.Fatalf("decode note: %v\nbody: %s", err, trunc(body))
	}
	id, err := asInt64(note["id"])
	if err != nil || id == 0 {
		t.Fatalf("facade note id: %v (body: %s)", err, trunc(body))
	}

	deadline := time.Now().Add(90 * time.Second)
	for {
		if strings.HasPrefix(noteTitle(t, id), "[stamped] ") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("note %d was never stamped — NotesFacade.CreateNoteChecked declares "+
				"(w17.event_emit), so NoteChecked should have reached the subscriber. "+
				"An unstamped title means the facade published nothing: the business "+
				"bundle either has no emit wrapper or never installs it.", id)
		}
		time.Sleep(time.Second)
	}
}

// noteTitle reads one note's current title over REST.
func noteTitle(t *testing.T, noteID int64) string {
	t.Helper()
	code, body, _ := get(t, fmt.Sprintf("%s/api/v1/notes/%d", *restTarget, noteID))
	if code != http.StatusOK {
		t.Fatalf("GET /notes/%d: got %d, want 200\nbody: %s", noteID, code, trunc(body))
	}
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode note: %v\nbody: %s", err, trunc(body))
	}
	title, _ := resp["title"].(string)
	return title
}

// TestComposed_BusinessTierReadsAnUploadedFile: the gateway WRITES the
// object and stores its handle, and the business tier follows that handle
// to the bytes.
//
// Both assertions are over the file's CONTENT (its size and its first
// line), which the attachment row cannot supply — so they hold only if
// the facade really opened the object. That is what separates this from
// asserting the upload succeeded.
//
// It is also the check that the two tiers resolved ONE bucket root. They
// are one process here and still read the root from env independently
// (the gateway for its driver, the bundle for the facade's store); two
// spellings of that variable would leave the upload on disk and this call
// answering 404.
func TestComposed_BusinessTierReadsAnUploadedFile(t *testing.T) {
	id := createNote(t, "attachment proof")
	const content = "first line of the statement\nsecond line\n"
	handle := attach(t, id, "statement.txt", content)

	code, body, _ := get(t, fmt.Sprintf("%s/api/v1/notes/%d/attachment/summary", *restTarget, id))
	if code != http.StatusOK {
		t.Fatalf("GET /notes/%d/attachment/summary: got %d, want 200\nbody: %s\n"+
			"A 404 here with the upload accepted means the facade's store and the gateway's driver\n"+
			"are rooted at different directories — one wrote, the other looked somewhere else.", id, code, trunc(body))
	}
	var sum map[string]any
	if err := json.Unmarshal(body, &sum); err != nil {
		t.Fatalf("decode summary: %v\nbody: %s", err, trunc(body))
	}
	if got, _ := sum["first_line"].(string); got != "first line of the statement" {
		t.Errorf("first_line = %q, want %q — the facade did not read the object's content", got, "first line of the statement")
	}
	n, err := asInt64(sum["byte_count"])
	if err != nil {
		t.Fatalf("byte_count: %v (body: %s)", err, trunc(body))
	}
	if n != int64(len(content)) {
		t.Errorf("byte_count = %d, want %d", n, len(content))
	}

	// The same object through the GATEWAY's own driver. Reading it here
	// too is what makes "one root" an assertion rather than an inference:
	// the download streams from the writer's driver, the summary above
	// from the business tier's reader.
	code, dl, _ := get(t, *restTarget+handle)
	if code != http.StatusOK {
		t.Fatalf("GET %s (download endpoint): got %d, want 200\nbody: %s", handle, code, trunc(dl))
	}
	if string(dl) != content {
		t.Errorf("download body = %q, want %q", trunc(dl), content)
	}
}

// TestComposed_BusinessTierStoresAFileItMade: the facade renders a file,
// puts it on its own connection (`app-exports`, `upload.writer: BUSINESS`)
// and records the handle; the answer carries the download URL, and the
// GATEWAY serves the bytes from that connection with its download headers.
//
// The facade writes through the store its bundle built from the bucket-root
// variable, the gateway reads through the driver it built from the same
// variable — one process, two resolutions — so a mismatch is a 404 here.
func TestComposed_BusinessTierStoresAFileItMade(t *testing.T) {
	id := createNote(t, "export proof")
	code, body, _ := post(t, fmt.Sprintf("%s/api/v1/notes/%d/export", *restTarget, id), map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("POST /notes/%d/export: got %d, want 200\nbody: %s", id, code, trunc(body))
	}
	var row map[string]any
	if err := json.Unmarshal(body, &row); err != nil {
		t.Fatalf("decode export: %v\nbody: %s", err, trunc(body))
	}
	file, _ := row["file"].(string)
	if !strings.HasPrefix(file, "/api/v1/notes/attachments/note-exports/") {
		t.Fatalf("file = %q, want a download URL under the business connection's bucket path", file)
	}
	resp := fetch(t, *restTarget+file, nil)
	want := "export proof\n\nb\n"
	if resp.code != http.StatusOK || string(resp.body) != want {
		t.Fatalf("GET %s: got %d %q, want 200 %q", file, resp.code, trunc(resp.body), want)
	}
	if resp.header.Get("ETag") == "" {
		t.Errorf("the download answered no ETag: the file is not served with the download endpoint's cache semantics")
	}
}

// TestComposed_RawResponseIsTheFile: an endpoint declared `raw_response`
// answers with the handler's bytes as the body — its type, its file name,
// a strong ETag — and a revalidation naming that ETag is a 304.
func TestComposed_RawResponseIsTheFile(t *testing.T) {
	id := createNote(t, "raw proof")
	url := fmt.Sprintf("%s/api/v1/notes/%d/text", *restTarget, id)
	resp := fetch(t, url, nil)
	if resp.code != http.StatusOK {
		t.Fatalf("GET %s: got %d, want 200\nbody: %s", url, resp.code, trunc(resp.body))
	}
	if want := "raw proof\n\nb\n"; string(resp.body) != want {
		t.Errorf("body = %q, want %q — the bytes, not a JSON document", trunc(resp.body), want)
	}
	for k, want := range map[string]string{
		"Content-Type":        "text/plain; charset=utf-8",
		"Content-Disposition": fmt.Sprintf("inline; filename=note-%d.txt", id),
		"Cache-Control":       "private, max-age=0, must-revalidate",
	} {
		if got := resp.header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	etag := resp.header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	again := fetch(t, url, map[string]string{"If-None-Match": etag})
	if again.code != http.StatusNotModified || len(again.body) != 0 {
		t.Errorf("revalidation: got %d with %d bytes, want 304 and none", again.code, len(again.body))
	}
}

type fetched struct {
	code   int
	body   []byte
	header http.Header
}

func fetch(t *testing.T, url string, hdr map[string]string) fetched {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request %s: %v", url, err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body %s: %v", url, err)
	}
	return fetched{code: resp.StatusCode, body: body, header: resp.Header}
}

// attach uploads a file against a note through the multipart endpoint and
// returns the download URL the gateway rewrote the stored handle into.
func attach(t *testing.T, noteID int64, filename, content string) string {
	t.Helper()
	var buf strings.Builder
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("multipart part: %v", err)
	}
	if _, err := io.WriteString(part, content); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	url := fmt.Sprintf("%s/api/v1/notes/%d/attachment", *restTarget, noteID)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(buf.String()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: got %d, want 200\nbody: %s", url, resp.StatusCode, trunc(body))
	}
	var row map[string]any
	if err := json.Unmarshal(body, &row); err != nil {
		t.Fatalf("decode attachment: %v\nbody: %s", err, trunc(body))
	}
	// The response carries the DOWNLOAD URL, not the raw handle: the
	// endpoint covering this field rewrites it (URL_FORM_RELATIVE), which
	// is the whole reason the parser demands one.
	stored, _ := row["stored_path"].(string)
	if stored == "" {
		t.Fatalf("AttachNote returned no stored_path (body: %s)", trunc(body))
	}
	if !strings.HasPrefix(stored, "/api/v1/notes/attachments/") {
		t.Fatalf("stored_path = %q, want a URL under the download endpoint's prefix", stored)
	}
	return stored
}

// ownerID is a fixed UUID — the Note model's owner_id has no FK (this
// example activates the auth plugin only to satisfy the admin surface's
// auth requirement), so any well-formed UUID works.
const ownerID = "0ad10000-0000-7000-8000-0000000000e1"

func createNote(t *testing.T, title string) int64 {
	t.Helper()
	code, body, _ := post(t, *restTarget+"/api/v1/notes", map[string]any{
		"owner_id": ownerID,
		"title":    title,
		"body":     "b",
	})
	if code != http.StatusOK {
		t.Fatalf("POST /notes: got %d, want 200\nbody: %s", code, trunc(body))
	}
	var note map[string]any
	if err := json.Unmarshal(body, &note); err != nil {
		t.Fatalf("decode note: %v\nbody: %s", err, trunc(body))
	}
	id, err := asInt64(note["id"])
	if err != nil {
		t.Fatalf("note id: %v (body: %s)", err, trunc(body))
	}
	// An absent field decodes as zero (see asInt64), and Note.id is an
	// IDENTITY pk — so zero here means the response carried no id at all,
	// not a note numbered 0.
	if id == 0 {
		t.Fatalf("CreateNote returned no id (body: %s)", trunc(body))
	}
	return id
}

func deliveries(t *testing.T, noteID int64) int64 {
	t.Helper()
	code, body, _ := get(t, fmt.Sprintf("%s/api/v1/notes/%d/deliveries", *restTarget, noteID))
	if code != http.StatusOK {
		t.Fatalf("GET /notes/%d/deliveries: got %d, want 200\nbody: %s", noteID, code, trunc(body))
	}
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode deliveries: %v\nbody: %s", err, trunc(body))
	}
	n, err := asInt64(resp["deliveries"])
	if err != nil {
		t.Fatalf("deliveries count: %v (body: %s)", err, trunc(body))
	}
	return n
}

// asInt64 accepts both wire shapes a 64-bit integer can arrive in. w17's
// JSON dialect carries int64 as a DECIMAL STRING (only Paging.total is
// carved out as a number), so the string branch is the expected one; the
// number branch keeps a dialect change from turning this helper into the
// thing that fails.
//
// An ABSENT field is zero, not an error: the surface marshals with
// EmitDefaultValues:false, so a proto3 zero has no key at all — a delivery
// count of 0 arrives as the empty object `{}`. That is the same rule that
// makes a zero-valued enum absent on both wires.
func asInt64(v any) (int64, error) {
	switch x := v.(type) {
	case string:
		return strconv.ParseInt(x, 10, 64)
	case float64:
		return int64(x), nil
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("unexpected type %T", v)
	}
}

func get(t *testing.T, url string) (int, []byte, string) {
	t.Helper()
	return do(t, http.MethodGet, url, nil)
}

func post(t *testing.T, url string, body map[string]any) (int, []byte, string) {
	t.Helper()
	return do(t, http.MethodPost, url, body)
}

func do(t *testing.T, method, url string, body map[string]any) (int, []byte, string) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, url, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body %s %s: %v", method, url, err)
	}
	return resp.StatusCode, out, resp.Header.Get("Content-Type")
}

func trunc(b []byte) string {
	const max = 400
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}
