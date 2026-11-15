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
//   - GET /_stub/calls/{id} reports how many requests an id received;
//   - POST /registry is a SOAP 1.1 register of VAT payers (registry.wsdl):
//     it requires the operation's SOAPAction, answers the status of each
//     DIC asked for, a Client fault for the DIC `bad`, and a Server fault
//     with 503 to every first request for `flaky` — the operation is
//     generated idempotent, so the client's retry turns it into a success.
package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
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
	mux.HandleFunc("POST /registry", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("SOAPAction") != `"urn:example:registry/getStatus"` {
			soapFault(w, http.StatusInternalServerError, "Client", "unknown SOAPAction "+r.Header.Get("SOAPAction"))
			return
		}
		var env struct {
			Body struct {
				Request struct {
					DIC []string `xml:"urn:example:registry dic"`
				} `xml:"urn:example:registry StatusRequest"`
			} `xml:"http://schemas.xmlsoap.org/soap/envelope/ Body"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := xml.Unmarshal(body, &env); err != nil || len(env.Body.Request.DIC) == 0 {
			soapFault(w, http.StatusInternalServerError, "Client", "no StatusRequest with a dic")
			return
		}
		var payers strings.Builder
		for _, dic := range env.Body.Request.DIC {
			mu.Lock()
			calls["registry:"+dic]++
			n := calls["registry:"+dic]
			mu.Unlock()
			switch {
			case dic == "bad":
				soapFault(w, http.StatusInternalServerError, "Client", "bad is not a DIC")
				return
			case dic == "flaky" && n%2 == 1:
				soapFault(w, http.StatusServiceUnavailable, "Server", "the register is busy")
				return
			}
			unreliable, account := "NE", `<r:account><r:domestic number="19-2000145399" bank="0800"/></r:account>`
			if dic == "unreliable" {
				unreliable, account = "ANO", `<r:account><r:iban>CZ6508000000192000145399</r:iban></r:account>`
			}
			fmt.Fprintf(&payers, `<r:payer dic="%s" unreliable="%s"><r:checkedAt>2026-11-15T10:00:00Z</r:checkedAt>%s<r:deposit currency="CZK">1250.50</r:deposit></r:payer>`,
				xmlText(dic), unreliable, account)
		}
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>`+
			`<r:StatusResponse xmlns:r="urn:example:registry" generated="2026-11-15">%s</r:StatusResponse></s:Body></s:Envelope>`, payers.String())
	})
	log.Fatal(http.ListenAndServe(":8080", mux))
}

func soapFault(w http.ResponseWriter, status int, code, reason string) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>`+
		`<s:Fault><faultcode>s:%s</faultcode><faultstring>%s</faultstring></s:Fault></s:Body></s:Envelope>`, code, xmlText(reason))
}

func xmlText(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
