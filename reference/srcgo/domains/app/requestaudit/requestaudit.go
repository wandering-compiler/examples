// Package requestaudit is the app domain's request event consumers: an audit
// trail of every recorded request and a usage log of the signed-in ones, both
// kept in Redis. The domain's generated subscriber runs them
// (subscribers.proto, `request_consumers`), each on its own durable consumer
// of the request stream.
//
// Each is a Redis list, one JSON entry per record: who sent which request,
// how it ended, and the request itself as the record carries it — which is
// without its sensitive fields, since those never reach the stream. A usage
// entry carries the caller's marketing consent when the project's auth keeps
// one; the reference's does not, and a tool that sends marketing messages
// would ask before it sends.
// The request is decoded with the domain's own types, which the subscriber
// binary links (its event payloads are in the same packages); this package
// imports none of them, so it does not register a second copy.
// The live suite (livetest/) reads both lists back.
package requestaudit

import (
	"context"
	"encoding/json"
	"os"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/wandering-compiler/sdk/go/lib/eventlog"
)

// envRedisAddr is the Redis the lists are kept in (host:port).
const envRedisAddr = "APP_REQUEST_AUDIT_REDIS_ADDR"

// Key is the Redis list the audit trail is appended to; UsageKey the one the
// usage log is.
const (
	Key      = "reference:request-audit"
	UsageKey = "reference:request-usage"
)

// Entry is one record of a list.
type Entry struct {
	API              string          `json:"api"`
	Method           string          `json:"method"`
	UserID           string          `json:"user_id,omitempty"`
	HTTPStatus       int32           `json:"http_status"`
	MarketingConsent *bool           `json:"marketing_consent,omitempty"`
	Request          json.RawMessage `json:"request,omitempty"`
	Response         json.RawMessage `json:"response,omitempty"`
}

// RegisterRequestConsumers registers the trail and the usage log. Without a
// Redis address it registers nothing: the stream keeps the records for a
// consumer that comes later.
func RegisterRequestConsumers(c *eventlog.Consumers) error {
	addr := os.Getenv(envRedisAddr)
	if addr == "" {
		return nil
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	if err := c.Audit("trail", list{rdb: rdb, key: Key}); err != nil {
		return err
	}
	return c.Marketing("usage", list{rdb: rdb, key: UsageKey})
}

// list appends a batch's records to one Redis list. It serves both purposes;
// each registration receives only the records of its own.
type list struct {
	rdb *redis.Client
	key string
}

func (l list) ConsumeAudit(ctx context.Context, b *eventlog.ConsumedBatch) error {
	return l.append(ctx, b)
}

func (l list) ConsumeMarketing(ctx context.Context, b *eventlog.ConsumedBatch) error {
	return l.append(ctx, b)
}

// append adds the batch's records in one call: either the whole batch lands
// or none of it does, and a batch offered again is appended again — the
// lists are at-least-once, as the stream is.
func (l list) append(ctx context.Context, b *eventlog.ConsumedBatch) error {
	entries := make([]any, 0, len(b.Records))
	for _, r := range b.Records {
		e := Entry{
			API:              r.Meta.GetApi().String(),
			Method:           r.Meta.GetMethod(),
			UserID:           r.Meta.GetUserId(),
			HTTPStatus:       r.Meta.GetHttpStatus(),
			MarketingConsent: r.Meta.MarketingConsent,
		}
		var err error
		if e.Request, err = marshal(r.Request); err != nil {
			return err
		}
		if e.Response, err = marshal(r.Response); err != nil {
			return err
		}
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		entries = append(entries, line)
	}
	if len(entries) == 0 {
		return nil
	}
	return l.rdb.RPush(ctx, l.key, entries...).Err()
}

// marshal renders a decoded request or response as JSON; nil when the record
// carries none or this binary does not link its type.
func marshal(m proto.Message) (json.RawMessage, error) {
	if m == nil {
		return nil, nil
	}
	return protojson.Marshal(m)
}
