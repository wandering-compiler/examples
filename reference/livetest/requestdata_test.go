package livetest

// Request data leaving the request: where each class of field may travel.
// ReportClientFault (users_business.proto) takes one field of each class —
// an unlabelled message, a gdpr contact e-mail and two sensitive decoys, a
// `type: SECRET` device secret and a hand-labelled `sensitive` plan — and its
// handler reports the fault to error tracking with the whole request. Three
// places are read back:
//
//   - the error report, at a fake Sentry this package serves on the stack's
//     network (compose.yaml points the business tier's W17_SENTRY_DSN at it);
//   - the request event stream, read off JetStream as bytes;
//   - the domain's own request consumers, an audit trail and a usage log in
//     Redis (the app domain's requestaudit package).
//
// In each the message and the e-mail are present and neither decoy is.

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/redis/go-redis/v9"

	"github.com/wandering-compiler/sdk/go/lib/eventlog"
	w17pb "github.com/wandering-compiler/sdk/go/pb/w17"
)

var (
	natsURL   = env("NATS_URL", "nats://nats:4222")
	redisAddr = env("AUDIT_REDIS_ADDR", "app-redis:6379")
	// sentryAddr is where the fake Sentry listens; run.sh gives this
	// package's container the name the stack's DSN dials.
	sentryAddr = env("FAKE_SENTRY_ADDR", ":8090")
)

const faultMethod = "/e2e.app.users.UsersService/ReportClientFault"

// sentry is the fake Sentry: every envelope it was sent, as bytes.
var sentry = &fakeSentry{}

type fakeSentry struct {
	mu        sync.Mutex
	envelopes [][]byte
}

// startFakeSentry listens for error reports, the way Sentry's ingest does:
// one envelope per POST to /api/<project>/envelope/.
func startFakeSentry() error {
	ln, err := net.Listen("tcp", sentryAddr)
	if err != nil {
		return err
	}
	go func() {
		_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body io.Reader = r.Body
			if r.Header.Get("Content-Encoding") == "gzip" {
				zr, err := gzip.NewReader(r.Body)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				body = zr
			}
			raw, err := io.ReadAll(body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			sentry.mu.Lock()
			sentry.envelopes = append(sentry.envelopes, raw)
			sentry.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}))
	}()
	return nil
}

// sentryEvent is the part of a Sentry event the assertions read. sentry-go
// sends `exception` as a bare list of exceptions.
type sentryEvent struct {
	Exception []struct {
		Value string `json:"value"`
	} `json:"exception"`
	Tags     map[string]string `json:"tags"`
	Contexts struct {
		Request struct {
			Method string         `json:"method"`
			Type   string         `json:"type"`
			Data   map[string]any `json:"data"`
		} `json:"request"`
	} `json:"contexts"`
}

// eventFor waits for the report whose exception names reportID and returns
// it with the whole envelope it came in.
func (s *fakeSentry) eventFor(t *testing.T, reportID string) (sentryEvent, []byte) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		s.mu.Lock()
		envs := append([][]byte(nil), s.envelopes...)
		s.mu.Unlock()
		for _, env := range envs {
			// An envelope is newline-separated JSON: its header, then a
			// header and a payload per item.
			sc := bufio.NewScanner(bytes.NewReader(env))
			sc.Buffer(nil, 1<<22)
			for sc.Scan() {
				var ev sentryEvent
				if json.Unmarshal(sc.Bytes(), &ev) != nil {
					continue
				}
				for _, v := range ev.Exception {
					if strings.Contains(v.Value, reportID) {
						return ev, env
					}
				}
			}
		}
		if time.Now().After(deadline) {
			var heads []string
			for _, env := range envs {
				heads = append(heads, fmt.Sprintf("%.400s", env))
			}
			t.Fatalf("no error report for %s reached the fake Sentry; %d envelopes arrived:\n%s", reportID, len(envs), strings.Join(heads, "\n---\n"))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// fault is one ReportClientFault request; every value carries the run's
// nonce, so an assertion finds this request's and no other's.
type fault struct {
	Message      string `json:"message"`
	ContactEmail string `json:"contact_email"`
	DeviceSecret string `json:"device_secret"`
	PlanTerms    string `json:"plan_terms"`
	ContactName  string `json:"contact_name"`
}

func newFault(nonce string) fault {
	return fault{
		Message:      "the editor crashed " + nonce,
		ContactEmail: "fault-" + nonce + "@reference.example",
		DeviceSecret: "SECRET-decoy-" + nonce,
		PlanTerms:    "PLAN-decoy-" + nonce + " 1299 EUR/month",
		ContactName:  "Ada " + nonce,
	}
}

// decoys are the values that must never leave a request.
func (f fault) decoys() []string { return []string{f.DeviceSecret, f.PlanTerms} }

func reportFault(t *testing.T, token string, f fault) string {
	t.Helper()
	body, _ := json.Marshal(f)
	raw := send(t, http.MethodPost, "/api/v1/context/faults", token, "application/json", bytes.NewReader(body), http.StatusOK)
	var resp struct {
		ReportID string `json:"report_id"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || resp.ReportID == "" {
		t.Fatalf("fault report answer %s: %v", raw, err)
	}
	return resp.ReportID
}

func nonce() string { return fmt.Sprintf("%d", time.Now().UnixNano()) }

func assertAbsent(t *testing.T, where string, data []byte, decoys []string) {
	t.Helper()
	for _, d := range decoys {
		if bytes.Contains(data, []byte(d)) {
			t.Errorf("%s carries the sensitive value %q", where, d)
		}
	}
}

// The error report carries the request it was raised for: the method, the
// caller, the message and the gdpr e-mail — and neither sensitive decoy,
// anywhere in what was sent.
func TestRequestData_TheErrorReportCarriesTheRequestWithoutItsSensitiveFields(t *testing.T) {
	token := signUp(t)
	f := newFault(nonce())
	id := reportFault(t, token, f)

	ev, envelope := sentry.eventFor(t, id)
	assertAbsent(t, "the error report", envelope, f.decoys())

	req := ev.Contexts.Request
	if req.Method != faultMethod || req.Type != "e2e.app.users.ReportClientFaultReq" {
		t.Errorf("request context names %q (%s), want %s", req.Method, req.Type, faultMethod)
	}
	if req.Data["message"] != f.Message || req.Data["contact_email"] != f.ContactEmail || req.Data["contact_name"] != f.ContactName {
		t.Errorf("request context data %v: want the message, the e-mail and the name", req.Data)
	}
	for _, k := range []string{"device_secret", "plan_terms"} {
		if _, ok := req.Data[k]; ok {
			t.Errorf("request context data has the sensitive field %s", k)
		}
	}
	if ev.Tags["method"] != faultMethod || ev.Tags["user_id"] == "" {
		t.Errorf("report tags %v: want the method and the caller's user_id", ev.Tags)
	}
}

// streamRecord is one record of the request event stream with its subject.
type streamRecord struct {
	subject string
	eventlog.ConsumedRecord
}

// readStream reads every message the request stream holds now, as bytes and
// decoded. This package links none of the domain's types, so a record's
// request and response are their bytes (RequestRaw / ResponseRaw).
func readStream(t *testing.T) (raw [][]byte, records []streamRecord) {
	t.Helper()
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("NATS: %v", err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream, err := js.Stream(ctx, eventlog.StreamName)
	if err != nil {
		t.Fatalf("stream %s: %v", eventlog.StreamName, err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for seq := info.State.FirstSeq; seq <= info.State.LastSeq && info.State.Msgs > 0; seq++ {
		m, err := stream.GetMsg(ctx, seq)
		if err != nil {
			continue // deleted by the stream's limits
		}
		raw = append(raw, m.Data)
		b, err := eventlog.DecodeBatch(m.Subject, m.Data, 0)
		if err != nil {
			t.Fatalf("batch %d on %s does not decode: %v", seq, m.Subject, err)
		}
		for _, r := range b.Records {
			records = append(records, streamRecord{m.Subject, r})
		}
	}
	return raw, records
}

// Every fault report of the run is in the request stream the moment it is
// answered — the method audits synchronously, so its record is stored before
// the response is written — with its caller, its status, the e-mail and the
// response; no message of the stream carries either decoy. A signed-in
// request serves audit and marketing, an anonymous one audit only, and no
// record carries a marketing consent: the auth plugin keeps none, and the
// stream records the request anyway.
func TestRequestData_TheStreamHoldsEveryRequestWithoutItsSensitiveFields(t *testing.T) {
	token := signUp(t)
	n := nonce()
	faults := make([]fault, 3)
	ids := make([]string, len(faults))
	for i := range faults {
		faults[i] = newFault(fmt.Sprintf("%s-%d", n, i))
		ids[i] = reportFault(t, token, faults[i])
	}

	// No wait: an asynchronous record would still be in its batch.
	raw, records := readStream(t)
	for _, m := range raw {
		for _, f := range faults {
			assertAbsent(t, "a stream message", m, f.decoys())
		}
	}
	for i, f := range faults {
		var got *streamRecord
		for j := range records {
			if r := &records[j]; r.Meta.GetMethod() == faultMethod && bytes.Contains(r.RequestRaw, []byte(f.ContactEmail)) {
				got = r
				break
			}
		}
		if got == nil {
			t.Errorf("fault report %d is not in the stream when it is answered", i)
			continue
		}
		m := got.Meta
		if got.subject != "w17.requests.app.rest" || m.GetApi() != w17pb.RequestApi_REQUEST_API_REST || m.GetDomain() != "app" {
			t.Errorf("record on %s: api %v domain %q", got.subject, m.GetApi(), m.GetDomain())
		}
		if m.GetUserId() == "" || m.GetHttpStatus() != http.StatusOK || m.GetRoute() == "" {
			t.Errorf("record meta %v: want the caller, status 200 and the route", m)
		}
		if !bytes.Contains(got.RequestRaw, []byte(f.Message)) || !bytes.Contains(got.RequestRaw, []byte(f.ContactName)) ||
			!bytes.Contains(got.ResponseRaw, []byte(ids[i])) {
			t.Errorf("record of fault %d lacks its message, its name or its response", i)
		}
		if p := m.GetPurposes(); len(p) != 2 || p[0] != w17pb.RequestPurpose_REQUEST_PURPOSE_AUDIT || p[1] != w17pb.RequestPurpose_REQUEST_PURPOSE_MARKETING {
			t.Errorf("record purposes %v, want audit and marketing", p)
		}
	}

	// The suite before this package sent anonymous requests too (failed
	// sign-ins, reads without a token): audit records them, marketing does not.
	anonymous := 0
	for _, r := range records {
		m := r.Meta
		if m.MarketingConsent != nil {
			t.Errorf("record of %s carries a marketing consent the auth plugin does not keep: %v", m.GetMethod(), m)
		}
		if r.subject != "w17.requests.app.rest" || m.GetUserId() != "" {
			continue
		}
		anonymous++
		if p := m.GetPurposes(); len(p) != 1 || p[0] != w17pb.RequestPurpose_REQUEST_PURPOSE_AUDIT {
			t.Errorf("anonymous record of %s: purposes %v, want audit only", m.GetMethod(), p)
		}
	}
	if anonymous == 0 {
		t.Errorf("no anonymous REST request in the stream (%d records)", len(records))
	}

	// The suite that ran before this package drove the admin too, and it
	// records. (Its MCP scenarios are skipped — the surface binds tokens to
	// device keys — so no MCP request is made here to record.)
	admin := false
	for _, r := range records {
		admin = admin || (r.subject == "w17.requests.app.admin" && r.Meta.GetApi() == w17pb.RequestApi_REQUEST_API_ADMIN)
	}
	if !admin {
		t.Errorf("no admin request in the stream (%d records)", len(records))
	}

	// The suite called EchoRequestContext (cases 0501–0505); it is excluded
	// from the stream, and no record of it is there.
	for _, r := range records {
		if r.Meta.GetMethod() == "/e2e.app.users.UsersService/EchoRequestContext" {
			t.Errorf("EchoRequestContext is excluded and was recorded: %v", r.Meta)
			break
		}
	}
}

// The domain's request consumers, decoding with the method's own types, get
// the fault report as JSON, with the e-mail and without the decoys: the audit
// trail and the usage log (marketing), whose entry carries no consent.
func TestRequestData_TheDomainsConsumersKeepTheTrailAndTheUsage(t *testing.T) {
	token := signUp(t)
	f := newFault(nonce())
	id := reportFault(t, token, f)

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	defer rdb.Close()
	for _, key := range []string{"reference:request-audit", "reference:request-usage"} {
		t.Run(key, func(t *testing.T) {
			line := waitForEntry(t, rdb, key, f.ContactEmail)
			assertAbsent(t, key, []byte(line), f.decoys())
			var e struct {
				API              string `json:"api"`
				Method           string `json:"method"`
				UserID           string `json:"user_id"`
				MarketingConsent *bool  `json:"marketing_consent"`
				Request          struct {
					Message      string `json:"message"`
					ContactEmail string `json:"contactEmail"`
				} `json:"request"`
				Response struct {
					ReportID string `json:"reportId"`
				} `json:"response"`
			}
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				t.Fatalf("entry %s: %v", line, err)
			}
			if e.Method != faultMethod || e.API != "REQUEST_API_REST" || e.UserID == "" || e.MarketingConsent != nil ||
				e.Request.Message != f.Message || e.Request.ContactEmail != f.ContactEmail || e.Response.ReportID != id {
				t.Errorf("entry %s", line)
			}
		})
	}
}

// waitForEntry returns the first entry of the list that contains needle,
// waiting for the consumer to append it.
func waitForEntry(t *testing.T, rdb *redis.Client, key, needle string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		lines, err := rdb.LRange(context.Background(), key, 0, -1).Result()
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		for _, line := range lines {
			if strings.Contains(line, needle) {
				return line
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the fault report never reached %s (%d entries)", key, len(lines))
		}
		time.Sleep(200 * time.Millisecond)
	}
}
