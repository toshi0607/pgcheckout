package pg

import "testing"

func TestSettingValue(t *testing.T) {
	for _, tc := range []struct {
		name, value, want string
		fail              bool
	}{
		{"Search_Path", `"$user", public`, `"$user", public`, false},
		{"datestyle", "ISO, MDY", "ISO, MDY", false},
		{"temp_tablespaces", "", "''", false},
		{"statement_timeout", "250ms", "'250ms'", false},
		{"custom.value", "a=b's", "'a=b''s'", false},
		{"custom.value", "a\x00b", "", true},
		{"search_path", "public; SELECT 1", "", true},
		{"search_path", "a'b", "", true},
	} {
		got, err := settingValue(tc.name, tc.value)
		if (err != nil) != tc.fail || got != tc.want {
			t.Errorf("settingValue(%q, %q) = %q, %v, want %q, failure=%v", tc.name, tc.value, got, err, tc.want, tc.fail)
		}
	}
}
