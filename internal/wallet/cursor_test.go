package wallet

import "testing"

// Cursors come back from clients, so anything at all can arrive. decodeCursor
// must never panic, and must only accept cursors that encodeCursor could have
// written: anything else, a client should hear is invalid.
func FuzzDecodeCursor(f *testing.F) {
	f.Add("")
	f.Add(encodeCursor(1))
	f.Add(encodeCursor(1_234_567))
	f.Fuzz(func(t *testing.T, cursor string) {
		id, err := decodeCursor(cursor)
		if err != nil || cursor == "" {
			return // refused, or the first page
		}
		if id <= 0 || encodeCursor(id) != cursor {
			t.Errorf("decodeCursor(%q) = %d, but we'd write %d as %q", cursor, id, id, encodeCursor(id))
		}
	})
}
