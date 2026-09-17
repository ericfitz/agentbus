package bus

import "strings"

// rankDigits is the rank alphabet in ASCII (byte) order, so comparing keys
// as strings compares them as base-62 fractions.
const rankDigits = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// rankBetween returns a key strictly between a and b in byte order; ""
// means unbounded on that side. Keys are base-62 fractions ("V" is about
// one half), and a key can always grow by a digit, so a midpoint always
// exists and sibling ranks never need rebalancing. Keys never end in '0':
// nothing sorts between "x" and "x0". Precondition: a < b when both are set.
// A byte outside rankDigits (only reachable from a corrupt or hand-written
// row, since edit_memory refuses tasks/ channels) is clamped to 0 rather
// than indexing IndexByte's -1.
func rankBetween(a, b string) string {
	var out strings.Builder
	for i := 0; ; i++ {
		lo := 0
		if i < len(a) {
			lo = strings.IndexByte(rankDigits, a[i])
		}
		if lo < 0 {
			lo = 0
		}
		hi := len(rankDigits)
		if i < len(b) {
			hi = strings.IndexByte(rankDigits, b[i])
		}
		if hi < 0 {
			hi = 0
		}
		if hi-lo > 1 {
			out.WriteByte(rankDigits[(lo+hi)/2])
			return out.String()
		}
		// Adjacent or equal digits: keep a's digit and go one place deeper.
		// If the digits differed, the result is already below b, so b stops
		// bounding the remaining places.
		out.WriteByte(rankDigits[lo])
		if hi != lo {
			b = ""
		}
	}
}

// lastDigitIndex returns the alphabet index of s's last byte, floored at 1
// (never 0) so callers that decrement from it can never produce the
// reserved trailing '0'. A byte outside rankDigits (see rankBetween) floors
// the same way.
func lastDigitIndex(s string) int {
	idx := strings.IndexByte(rankDigits, s[len(s)-1])
	if idx < 1 {
		idx = 1
	}
	return idx
}

// rankAfter returns the smallest key strictly greater than a (a must be
// non-empty). Repeatedly appending at the end of a list calls this rather
// than rankBetween(a, ""): taking the midpoint against the alphabet's
// unbounded top edge approaches it geometrically, growing the key by a
// byte every few appends (~6, measured). Instead: increment a's last digit
// in place when it isn't already the alphabet's top ('z'); once it is,
// extend with a fresh digit rather than carrying into earlier digits, so
// growth is one byte per len(rankDigits)-1 appends instead of ~6. The fresh
// digit starts at rankDigits[1] (never 0, the reserved trailing digit),
// leaving the rest of the alphabet's range for the appends that follow.
func rankAfter(a string) string {
	idx := strings.IndexByte(rankDigits, a[len(a)-1])
	if idx < 0 {
		idx = 0
	}
	if idx < len(rankDigits)-1 {
		return a[:len(a)-1] + string(rankDigits[idx+1])
	}
	return a + string(rankDigits[1])
}

// rankBefore returns the largest key strictly less than b (b must be
// non-empty), symmetric to rankAfter for repeated prepends at the front of
// a list. Decrements b's last digit in place when that stays above the
// reserved '1' floor (lastDigitIndex never returns 0, so decrementing it is
// always safe); once it can't decrement further, extends by replacing the
// last digit with '0' followed by 'z': '0' sorts below any digit b could
// have there, and leading (non-trailing) '0' is fine, only a trailing one
// is reserved, leaving the full alphabet's range of decrements before the
// next extension.
func rankBefore(b string) string {
	idx := lastDigitIndex(b)
	if idx > 1 {
		return b[:len(b)-1] + string(rankDigits[idx-1])
	}
	return b[:len(b)-1] + string(rankDigits[0]) + string(rankDigits[len(rankDigits)-1])
}
