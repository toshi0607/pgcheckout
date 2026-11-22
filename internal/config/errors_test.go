package config

import "testing"

func TestRedactConnectionDetails(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"dial postgres://alice:s%40cret@localhost/db: refused", "dial <redacted-url> refused"},
		{`parse "postgresql://alice:secret@localhost/db name": invalid`, "parse <redacted-url>: invalid"},
		{"cannot parse `postgresql://localhost/db?options=-c x=y`: invalid", "cannot parse <redacted-url>: invalid"},
		{`host=localhost password=secret user=alice`, `host=localhost password=<redacted> user=alice`},
		{`password='a b\'c' password="d e" password=f\ g dbname=app`, `password=<redacted> password=<redacted> password=<redacted> dbname=app`},
		{`postgres://localhost/app?password=secret password=other`, `<redacted-url> password=<redacted>`},
		{"database is unavailable", "database is unavailable"},
	} {
		if got := RedactConnectionDetails(tc.input); got != tc.want {
			t.Errorf("RedactConnectionDetails(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
