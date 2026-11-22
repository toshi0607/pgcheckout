package pg

import (
	"testing"

	"github.com/toshi0607/pgcheckout/internal/config"
)

func TestSelectStrategy(t *testing.T) {
	for _, tc := range []struct {
		version int
		input   string
		output  string
	}{
		{140999, "auto", Default},
		{150000, "auto", WALLog},
		{179999, "auto", WALLog},
		{180000, "auto", Clone},
		{190000, "auto", Clone},
		{179999, "clone", ""},
		{180000, "clone", Clone},
		{149999, "file_copy", ""},
		{150000, "file_copy", FileCopy},
		{180000, "file_copy", FileCopy},
		{149999, "wal_log", ""},
		{150000, "wal_log", WALLog},
		{180000, "wal_log", WALLog},
		{140000, "default", Default},
		{180000, "default", Default},
		{180000, "unknown", ""},
	} {
		got, err := SelectStrategy(tc.version, tc.input)
		if tc.output == "" {
			if config.ErrorCode(err) != config.ConfigInvalid {
				t.Errorf("SelectStrategy(%d, %q) error = %v, want config_invalid", tc.version, tc.input, err)
			}
		} else if err != nil || got != tc.output {
			t.Errorf("SelectStrategy(%d, %q) = %q, %v, want %q", tc.version, tc.input, got, err, tc.output)
		}
	}
}
