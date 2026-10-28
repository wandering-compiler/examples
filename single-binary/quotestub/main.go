// Command quotestub stands in for the third-party Quotes API
// (openapi.yaml) in the test stack. It is what the app's egress client
// calls, so what it checks and answers is what the scenarios prove:
//
//   - every request must carry `Authorization: Bearer <QUOTES_TOKEN>` —
//     the credential the client takes from W17_CLIENT_QUOTES_DSN — or it
//     answers 401;
//   - GET /quotes/{id} answers the quote, or 404 for an unknown id;
//   - the id `flaky` answers 503 to every first request of a run and the
//     quote to the next: the client's retry is what turns it into a success;
//   - GET /_stub/calls/{id} reports how many requests an id received.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
)

var quotes = map[string]map[string]string{
	"q1":    {"id": "q1", "text": "Simple things should be simple.", "author": "Alan Kay"},
	"flaky": {"id": "flaky", "text": "Try again.", "author": "Unknown"},
}

func main() {
	token := os.Getenv("QUOTES_TOKEN")
	if token == "" {
		token = "test-token"
	}
	var mu sync.Mutex
	calls := map[string]int{}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /quotes/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "bad credential"})
			return
		}
		id := r.PathValue("id")
		mu.Lock()
		calls[id]++
		n := calls[id]
		mu.Unlock()
		if id == "flaky" && n%2 == 1 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "busy, try again"})
			return
		}
		q, ok := quotes[id]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "no quote " + id})
			return
		}
		writeJSON(w, http.StatusOK, q)
	})
	mux.HandleFunc("GET /_stub/calls/{id}", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]int{"calls": calls[strings.TrimSpace(r.PathValue("id"))]})
	})
	log.Fatal(http.ListenAndServe(":8080", mux))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
