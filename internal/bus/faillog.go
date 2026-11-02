package bus

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// failLogInterval is how long identical background failures are
	// suppressed after one is logged. The next one after it is logged with a
	// "suppressed" count of those held back.
	failLogInterval = 10 * time.Minute
	// failLogMaxKeys bounds the limiter's memory.
	failLogMaxKeys = 256
	failLogKeyMax  = 512
)

// failLimiter rate-limits repeated identical warnings. It has no timer: a
// repeat is judged against the bus clock (Bus.Now) when it happens, so it is
// deterministic under a test clock.
type failLimiter struct {
	mu   sync.Mutex
	seen map[string]*failState
}

type failState struct {
	last       time.Time
	suppressed int
}

// warnLimited logs a warning unless the same key was logged less than
// failLogInterval ago, in which case it counts the repeat. The first
// occurrence after the interval is logged with suppressed=N, the number of
// identical failures held back. key must identify the failure but not vary
// with incidental detail (row ids, elapsed time), or nothing is suppressed.
func (b *Bus) warnLimited(key, msg string, args ...any) {
	if len(key) > failLogKeyMax {
		key = key[:failLogKeyMax]
	}
	now := b.Now()
	l := &b.failLog
	l.mu.Lock()
	if l.seen == nil {
		l.seen = map[string]*failState{}
	}
	st, ok := l.seen[key]
	if ok && now.Sub(st.last) < failLogInterval {
		st.suppressed++
		l.mu.Unlock()
		return
	}
	suppressed := 0
	if ok {
		suppressed = st.suppressed
	} else if len(l.seen) >= failLogMaxKeys {
		for k, v := range l.seen {
			if now.Sub(v.last) >= failLogInterval {
				delete(l.seen, k)
			}
		}
		if len(l.seen) >= failLogMaxKeys {
			clear(l.seen)
		}
	}
	l.seen[key] = &failState{last: now}
	l.mu.Unlock()
	if suppressed > 0 {
		args = append(args, "suppressed", suppressed, "suppressed_note", "identical failures since the last log line")
	}
	b.log.Warn(msg, args...)
}

// logStepFailure reports a failed maintenance step with its name and how
// long it ran, rate-limited per step and error text.
func (b *Bus) logStepFailure(step string, err error, elapsed time.Duration) {
	b.warnLimited("maintenance:"+step+":"+err.Error(), "maintenance step failed",
		"step", step, "elapsed_ms", elapsed.Milliseconds(), "err", err)
}

// redactEndpoint drops userinfo and replaces query values, so an endpoint
// that carries credentials is safe to log. An unparsable endpoint is not
// logged at all.
func redactEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[unparsable endpoint]"
	}
	u.User = nil
	if q := u.Query(); len(q) > 0 {
		for k := range q {
			q[k] = []string{"REDACTED"}
		}
		u.RawQuery = q.Encode()
	}
	u.Fragment = ""
	return u.String()
}

// scrub removes the API key and the raw endpoint (which may hold userinfo or
// a key in its query) from text bound for a log or the status notice.
func (e *embedder) scrub(s string) string {
	if e.key != "" {
		s = strings.ReplaceAll(s, e.key, "[redacted]")
	}
	if e.endpoint != "" {
		s = strings.ReplaceAll(s, e.endpoint, redactEndpoint(e.endpoint))
	}
	return s
}

// logEmbedFailure logs a failed embedding pass with enough to diagnose it
// without reproducing it: the redacted endpoint, model, HTTP status and a
// bounded body excerpt (when the endpoint answered), the size of the failed
// request and the seqs of the rows in it. Never the key or request headers.
// A pass cancelled by its caller says nothing; one that ran out its deadline
// (a hung endpoint) is logged.
// Repeats of the same failure are rate-limited; row ids are not part of the
// identity, since they differ between otherwise identical passes.
func (b *Bus) logEmbedFailure(ctx context.Context, err error, rows []int64) {
	if err == nil || errors.Is(ctx.Err(), context.Canceled) || b.embedder == nil {
		return
	}
	e := b.embedder
	endpoint := redactEndpoint(e.endpoint)
	status, body := 0, ""
	var he *embedHTTPError
	if errors.As(err, &he) {
		status, body = he.code, he.body
	}
	text := e.errText(err)
	b.warnLimited("embed:"+endpoint+"|"+e.model+"|"+text, "embedding failed",
		"endpoint", endpoint, "model", e.model, "status", status, "body", body,
		"batch_size", len(rows), "row_ids", rows, "err", text)
}

// logEmbedRejected logs rows the endpoint refused on their own and that are
// now recorded in embed_failures (ADR 0018), so they are never retried. rejected
// maps an index into seqs to the endpoint's answer. Not rate-limited: each
// row is reported once.
func (b *Bus) logEmbedRejected(rejected map[int]string, seqs []int64) {
	var ids []int64
	first := ""
	for i, reason := range rejected {
		if i < len(seqs) {
			ids = append(ids, seqs[i])
			if first == "" {
				first = reason
			}
		}
	}
	if len(ids) == 0 {
		return
	}
	slices.Sort(ids)
	b.log.Warn("embedding rejected rows", "endpoint", redactEndpoint(b.embedder.endpoint), "model", b.embedder.model,
		"row_count", len(ids), "row_ids", ids, "err", b.embedder.scrub(first))
}

// errText is err's text, safe to log. net/http's *url.Error carries the
// request URL with the user name and query intact, so it is rebuilt around the
// redacted endpoint.
func (e *embedder) errText(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return e.scrub(fmt.Sprintf("%s %q: %v", ue.Op, redactEndpoint(e.endpoint), ue.Err))
	}
	return e.scrub(err.Error())
}
