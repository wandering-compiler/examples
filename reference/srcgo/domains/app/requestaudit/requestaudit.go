// Package requestaudit is the app domain's request event consumer: an audit
// trail of every recorded request, kept in Redis. The domain's generated
// subscriber runs it (subscribers.proto, `request_consumers`), on its own
// durable consumer of the request stream.
//
// The trail is a Redis list, one JSON entry per record: who sent which
// request, how it ended, and the request itself as the record carries it —
// which is without its sensitive fields, since those never reach the stream.
// The request is decoded with the domain's own types, which the subscriber
// binary links (its event payloads are in the same packages); this package
// imports none of them, so it does not register a second copy.
// The live suite (livetest/) reads the list back.
package requestaudit

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/wandering-compiler/sdk/go/lib/eventlog"
)

// envRedisAddr is the Redis the trail is kept in (host:port).
const envRedisAddr = "APP_REQUEST_AUDIT_REDIS_ADDR"

// Key is the Redis list the trail is appended to.
const Key = "reference:request-audit"

// Entry is one record of the trail.
type Entry struct {
	API        string          `json:"api"`
	Method     string          `json:"method"`
	UserID     string          `json:"user_id,omitempty"`
	HTTPStatus int32           `json:"http_status"`
	Request    json.RawMessage `json:"request,omitempty"`
	Response   json.RawMessage `json:"response,omitempty"`
}

// RegisterRequestConsumers registers the trail. Without a Redis address it
// registers nothing: the stream keeps the records for a consumer that comes
// later.
func RegisterRequestConsumers(c *eventlog.Consumers) error {
	addr := os.Getenv(envRedisAddr)
	if addr == "" {
		return nil
	}
	return c.Audit("trail", trail{rdb: redis.NewClient(&redis.Options{Addr: addr})})
}

type trail struct{ rdb *redis.Client }

// ConsumeAudit appends the batch's records in one call: either the whole
// batch lands or none of it does, and a batch offered again is appended
// again — the trail is at-least-once, as the stream is.
func (t trail) ConsumeAudit(ctx context.Context, b *eventlog.ConsumedBatch) error {
	entries := make([]any, 0, len(b.Records))
	for _, r := range b.Records {
		e := Entry{
			API:        r.Meta.GetApi().String(),
			Method:     r.Meta.GetMethod(),
			UserID:     r.Meta.GetUserId(),
			HTTPStatus: r.Meta.GetHttpStatus(),
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
	return t.rdb.RPush(ctx, Key, entries...).Err()
}

// marshal renders a decoded request or response as JSON; nil when the record
// carries none or this binary does not link its type.
func marshal(m proto.Message) (json.RawMessage, error) {
	if m == nil {
		return nil, nil
	}
	return protojson.Marshal(m)
}

// ConsumeMarketing is not registered for: the trail is audit only.
func (trail) ConsumeMarketing(context.Context, *eventlog.ConsumedBatch) error {
	return errors.New("requestaudit: the trail is not a marketing consumer")
}
