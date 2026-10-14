package livetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Business-to-business COMPOSITION, live: a facade method with no annotation
// of its own reuses CreateNoteChecked — which declares (w17.event_emit) =
// NoteChecked — through the server the registry returned. The observable is
// the "[stamped] " title the NoteChecked subscriber writes, which only that
// event produces.
//
// A direct Go call on the implementation would skip the emit wrapper: the
// note would be created and NoteChecked never published.

func createVia(t *testing.T, path, title string) (int, int64, []byte) {
	t.Helper()
	code, body, _ := post(t, *restTarget+"/api/v1/notes/"+path, map[string]any{
		"owner_id": ownerID,
		"title":    title,
		"body":     "b",
	})
	if code != http.StatusOK {
		return code, 0, body
	}
	var note map[string]any
	if err := json.Unmarshal(body, &note); err != nil {
		t.Fatalf("decode note: %v\nbody: %s", err, trunc(body))
	}
	id, err := asInt64(note["id"])
	if err != nil || id == 0 {
		t.Fatalf("note id: %v (body: %s)", err, trunc(body))
	}
	return code, id, body
}

func waitStamped(t *testing.T, id int64, what string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for !strings.HasPrefix(noteTitle(t, id), "[stamped] ") {
		if time.Now().After(deadline) {
			t.Fatalf("note %d was never stamped — %s", id, what)
		}
		time.Sleep(time.Second)
	}
}

func TestComposition_AComposedCallStillPublishesTheCalleesEvent(t *testing.T) {
	code, id, body := createVia(t, "checked-composed", "composed call")
	if code != http.StatusOK {
		t.Fatalf("POST /notes/checked-composed: %d\n%s", code, trunc(body))
	}
	waitStamped(t, id, "CreateNoteCheckedComposed reused CreateNoteChecked through the "+
		"served server, so its NoteChecked should have reached the subscriber; an unstamped "+
		"note means the composition skipped the emit wrapper")
}

func TestComposition_InsideATransactionTheEventFollowsTheCommit(t *testing.T) {
	code, id, body := createVia(t, "checked-in-tx", "composed in tx")
	if code != http.StatusOK {
		t.Fatalf("POST /notes/checked-in-tx: %d\n%s", code, trunc(body))
	}
	waitStamped(t, id, "the transaction committed, so the parked NoteChecked should have gone out")
}

// Rolled back AFTER the composed call succeeded: the composed write must not
// exist. (That the parked NoteChecked is dropped rather than published is not
// observable over REST: a stamp handler for a note that never existed just
// fails.) A committed
// composition right after it must still be stamped, so a broken commit path
// cannot pass this by publishing nothing at all.
func TestComposition_ARolledBackTransactionLeavesNoNote(t *testing.T) {
	marker := fmt.Sprintf("rollback:%d", time.Now().UnixNano())
	code, _, body := createVia(t, "checked-in-tx", marker)
	if code < 400 {
		t.Fatalf("the rolled-back composition answered %d: %s", code, trunc(body))
	}

	_, after, _ := createVia(t, "checked-in-tx", "committed after the rollback")
	waitStamped(t, after, "the committed transaction after the rolled-back one should be stamped")

	for _, n := range listNotes(t) {
		if title, _ := n["title"].(string); strings.Contains(title, marker) {
			t.Fatalf("the rolled-back note exists: %v — its write escaped the transaction", n)
		}
	}
}

func listNotes(t *testing.T) []map[string]any {
	t.Helper()
	code, body, _ := get(t, fmt.Sprintf("%s/api/v1/notes?owner_id=%s&limit=100", *restTarget, ownerID))
	if code != http.StatusOK {
		t.Fatalf("GET /notes: %d\n%s", code, trunc(body))
	}
	var resp struct {
		Notes []map[string]any `json:"notes"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode notes: %v\n%s", err, trunc(body))
	}
	return resp.Notes
}
