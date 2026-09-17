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
func rankBetween(a, b string) string {
	var out strings.Builder
	for i := 0; ; i++ {
		lo := 0
		if i < len(a) {
			lo = strings.IndexByte(rankDigits, a[i])
		}
		hi := len(rankDigits)
		if i < len(b) {
			hi = strings.IndexByte(rankDigits, b[i])
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
