package bus

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
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
	msg        string
	last       time.Time
	suppressed int
}

// NormText strips digits from error text used in a limiter key, so request
// ids, counts and retry-after values do not make identical failures look
// different.
func NormText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsDigit(r) {
			return -1
		}
		return r
	}, s)
}

func normText(s string) string { return NormText(s) }

// WarnLimited is warnLimited for other packages' background warnings, so one
// limiter (per process, per key) covers them.
func (b *Bus) WarnLimited(key, msg string, args ...any) { b.warnLimited(key, msg, args...) }

type failSummary struct {
	msg string
	n   int
}

// warnLimited logs a warning unless the same key was logged less than
// failLogInterval ago, in which case it counts the repeat. The first
// occurrence after the interval is logged with suppressed=N, the number of
// identical failures held back. When the table is full the oldest key is
// evicted, and Close flushes everything pending, each with a summary line, so
// a count is not lost. key must identify the failure but not vary with
// incidental detail (row ids, elapsed time, digits in text; see NormText).
// The limiter is per Bus (per process); another process logs its own.
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
	var evicted *failSummary
	if ok {
		suppressed = st.suppressed
	} else if len(l.seen) >= failLogMaxKeys {
		oldest := ""
		var ot time.Time
		for k, v := range l.seen {
			if oldest == "" || v.last.Before(ot) {
				oldest, ot = k, v.last
			}
		}
		if v := l.seen[oldest]; v.suppressed > 0 {
			evicted = &failSummary{v.msg, v.suppressed}
		}
		delete(l.seen, oldest)
	}
	l.seen[key] = &failState{msg: msg, last: now}
	l.mu.Unlock()
	if evicted != nil {
		b.logSummary(*evicted)
	}
	if suppressed > 0 {
		args = append(args, "suppressed", suppressed, "suppressed_note", "identical failures since the last log line")
	}
	b.log.Warn(msg, args...)
}

func (b *Bus) logSummary(s failSummary) {
	b.log.Warn(s.msg, "suppressed", s.n, "summary", true, "suppressed_note", "identical failures not logged individually")
}

// flushFailLog logs a summary for every key with held-back repeats. Close
// calls it, so a process that exits does not lose the counts.
func (b *Bus) flushFailLog() {
	l := &b.failLog
	l.mu.Lock()
	var out []failSummary
	for _, v := range l.seen {
		if v.suppressed > 0 {
			out = append(out, failSummary{v.msg, v.suppressed})
			v.suppressed = 0
		}
	}
	l.mu.Unlock()
	slices.SortFunc(out, func(x, y failSummary) int { return strings.Compare(x.msg, y.msg) })
	for _, s := range out {
		b.logSummary(s)
	}
}

// logStepFailure reports a failed maintenance step with its name and how
// long it ran, rate-limited per step and error text.
func (b *Bus) logStepFailure(step string, err error, elapsed time.Duration) {
	b.warnLimited("maintenance:"+step+":"+normText(err.Error()), "maintenance step failed",
		"step", step, "elapsed_ms", elapsed.Milliseconds(), "err", err)
}

// redactEndpoint drops userinfo, the fragment and the whole query string
// (whatever its shape: ?key=v, a bare ?token, malformed escapes, ; pairs), so
// an endpoint that carries credentials is safe to log. The path is kept as is
// so the log still shows which route failed; embedder.safeEndpoint also
// removes the API key from it. An unparsable endpoint is not logged at all.
func redactEndpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[unparsable endpoint]"
	}
	u.User = nil
	u.Fragment = ""
	u.RawFragment = ""
	if u.RawQuery != "" || u.ForceQuery {
		u.RawQuery = "REDACTED"
	}
	return u.String()
}

// safeEndpoint is the endpoint for logs: redacted, and without the API key
// should it appear in the path. Other path segments are logged as they are.
func (e *embedder) safeEndpoint() string {
	s := redactEndpoint(e.endpoint)
	if e.key != "" {
		s = strings.ReplaceAll(s, e.key, "[redacted]")
	}
	return s
}

// scrub removes the API key and the raw endpoint (which may hold userinfo or
// a key in its query) from text bound for a log or the status notice.
func (e *embedder) scrub(s string) string {
	if e.endpoint != "" {
		s = strings.ReplaceAll(s, e.endpoint, e.safeEndpoint())
	}
	if e.key != "" {
		s = strings.ReplaceAll(s, e.key, "[redacted]")
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
	endpoint := e.safeEndpoint()
	status, body := 0, ""
	var he *embedHTTPError
	if errors.As(err, &he) {
		status, body = he.code, he.body
	}
	text := e.errText(err)
	b.warnLimited("embed|"+endpoint+"|"+e.model+"|"+strconv.Itoa(status)+"|"+normText(text), "embedding failed",
		"endpoint", endpoint, "model", e.model, "status", status, "body", body,
		"batch_size", len(rows), "row_ids", rows, "err", text)
}

// logEmbedRejected logs rows the endpoint refused on their own and that are
// now recorded in embed_failures (ADR 0018), so they are never retried. rejected
// maps an index into seqs to the endpoint's answer. Not rate-limited: each
// row is reported once.
func (b *Bus) logEmbedRejected(rejected map[int]string, seqs []int64) {
	idx := make([]int, 0, len(rejected))
	for i := range rejected {
		if i < len(seqs) {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return
	}
	slices.Sort(idx)
	ids := make([]int64, 0, len(idx))
	counts := map[string]int{}
	for _, i := range idx {
		ids = append(ids, seqs[i])
		counts[rejected[i]]++
	}
	var reasons []string
	for r, n := range counts {
		reasons = append(reasons, fmt.Sprintf("%d x %s", n, r))
	}
	slices.Sort(reasons)
	b.log.Warn("embedding rejected rows", "endpoint", b.embedder.safeEndpoint(), "model", b.embedder.model,
		"row_count", len(ids), "row_ids", ids, "err", b.embedder.scrub(rejected[idx[0]]), "reasons", strings.Join(reasons, "; "))
}

// errText is err's text, safe to log, keeping any wrapper text around it.
// net/http's *url.Error carries the request URL (user name and query intact),
// so that URL is replaced inside the message by the redacted endpoint.
func (e *embedder) errText(err error) string {
	s := err.Error()
	var ue *url.Error
	if errors.As(err, &ue) && ue.URL != "" {
		s = strings.ReplaceAll(s, ue.URL, e.safeEndpoint())
	}
	return e.scrub(s)
}
