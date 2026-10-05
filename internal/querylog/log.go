// Package querylog persists bounded query diagnostics outside request execution.
package querylog

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/context4ai/sourcegraph/internal/contract"
	"github.com/context4ai/sourcegraph/internal/control"
)

const Limit = 1000

type Entry struct {
	ID                string    `json:"id" bson:"id"`
	Time              time.Time `json:"time" bson:"time"`
	Instance          string    `json:"instance" bson:"instance"`
	RequestID         string    `json:"request_id" bson:"request_id"`
	Transport         string    `json:"transport" bson:"transport"`
	Operation         string    `json:"operation" bson:"operation"`
	Actor             string    `json:"actor" bson:"actor"`
	Repository        string    `json:"repository" bson:"repository"`
	DurationMS        int64     `json:"duration_ms" bson:"duration_ms"`
	Code              string    `json:"code" bson:"code"`
	Message           string    `json:"message" bson:"message"`
	Context           string    `json:"context,omitempty" bson:"context"`
	ContextTruncated  bool      `json:"context_truncated" bson:"context_truncated"`
	QueryFragment     string    `json:"query_fragment,omitempty" bson:"query_fragment,omitempty"`
	SuggestedQuery    string    `json:"suggested_query,omitempty" bson:"suggested_query,omitempty"`
	SuggestionOmitted bool      `json:"suggestion_omitted,omitempty" bson:"suggestion_omitted,omitempty"`
}
type Store interface {
	Append(context.Context, []Entry) error
	Read(context.Context) ([]Entry, error)
	Clear(context.Context) error
}
type metadata struct{ ID, Transport string }
type metadataKey struct{}

func WithRequest(ctx context.Context, id, transport string) context.Context {
	return context.WithValue(ctx, metadataKey{}, metadata{id, transport})
}

var secrets = regexp.MustCompile(`(?i)(?:bearer\s+[^\s"\\]+|(?:code_pat_|sgk_)[a-z0-9_-]+|eyJ[a-z0-9_-]+\.[a-z0-9_-]+\.[a-z0-9_-]+|(?:token|password|secret|authorization|cookie)\s*[=:]\s*[^\s",}]+)`)

func clean(s string, n int) string {
	s = secrets.ReplaceAllString(s, "[REDACTED]")
	if len(s) > n {
		s = s[:n]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return strings.Clone(s)
}

type Log struct {
	Store         Store
	queue         chan Entry
	Dropped       atomic.Uint64
	WriteFailures atomic.Uint64
}

func New(store Store) *Log { return &Log{Store: store, queue: make(chan Entry, 256)} }

// Record does bounded CPU work and a nonblocking send. It never accesses storage.
func (l *Log) Record(ctx context.Context, instance, actor, repo, op string, start time.Time, input any, err error) {
	if l == nil || err == nil {
		return
	}
	code, message := "INTERNAL_ERROR", "Query failed; inspect service diagnostics."
	var ce *contract.Error
	if errors.As(err, &ce) {
		code, message = ce.Code, ce.Message
	} else if errors.Is(err, context.DeadlineExceeded) {
		code, message = "QUERY_TIMEOUT", "Query deadline exceeded."
	} else if errors.Is(err, context.Canceled) {
		code, message = "QUERY_CANCELED", "Query was canceled."
	}
	raw, _ := json.Marshal(input)
	details := clean(string(raw), 4096)
	m, _ := ctx.Value(metadataKey{}).(metadata)
	entry := Entry{ID: control.NewID(), Time: time.Now().UTC(), Instance: clean(instance, 128), RequestID: clean(m.ID, 128), Transport: clean(m.Transport, 16), Actor: clean(actor, 256), Repository: clean(repo, 512), Operation: op, DurationMS: time.Since(start).Milliseconds(), Code: clean(code, 128), Message: clean(message, 512), Context: details, ContextTruncated: len(raw) > 4096}
	if ce != nil {
		entry.QueryFragment = clean(ce.QueryFragment, 512)
		// A changed or truncated suggestion is not executable recovery guidance.
		if ce.SuggestedQuery != "" {
			entry.SuggestedQuery = clean(ce.SuggestedQuery, 2048)
			if entry.SuggestedQuery != ce.SuggestedQuery {
				entry.SuggestedQuery = ""
				entry.SuggestionOmitted = true
			}
		}
	}
	select {
	case l.queue <- entry:
	default:
		l.Dropped.Add(1)
	}
}
func (l *Log) Run(ctx context.Context, logger *slog.Logger) {
	if l == nil {
		return
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	pending := []Entry{}
	flush := func() {
		if len(pending) == 0 {
			return
		}
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			budget, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err = l.Store.Append(budget, pending)
			cancel()
			if err == nil {
				pending = nil
				return
			}
			l.WriteFailures.Add(1)
		}
		l.Dropped.Add(uint64(len(pending)))
		pending = nil
		logger.Warn("query_error_log_write_failed", "dropped", l.Dropped.Load())
	}
	for {
		select {
		case e := <-l.queue:
			pending = append(pending, e)
			if len(pending) >= 32 {
				flush()
			}
		case <-tick.C:
			flush()
		case <-ctx.Done():
			for {
				select {
				case e := <-l.queue:
					pending = append(pending, e)
				default:
					flush()
					return
				}
			}
		}
	}
}
