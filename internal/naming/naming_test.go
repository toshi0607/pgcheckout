package naming

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/toshi0607/pgcheckout/internal/config"
)

func TestBaseNameTrailingUnderscoreRejected(t *testing.T) {
	err := ValidateBase("a_")
	problem, ok := errors.AsType[*config.Error](err)
	if !ok || problem.Code != config.ConfigInvalid || problem.Hint != "base names must not end with an underscore" {
		t.Fatalf("ValidateBase(%q) = %#v", "a_", err)
	}
	if err := ValidateBase("a_b"); err != nil {
		t.Fatalf("ValidateBase(%q) = %v", "a_b", err)
	}
}

func TestSlug(t *testing.T) {
	for _, tc := range []struct {
		branch, want string
	}{
		{"main", "main"},
		{"feat/login", "feat_login_d668f008"},
		{"Feature/ABC", "feature_abc_f718f729"},
		{"Feat", "feat_8664d8a3"},
		{strings.Repeat("a", 100), strings.Repeat("a", 54) + "_28165978"},
		{"é", "___4a99557e"},
		{"feat_x", "feat_x"},
		{"feat/x", "feat_x_79b4cc55"},
	} {
		t.Run(tc.branch, func(t *testing.T) {
			if got := Slug(tc.branch); got != tc.want {
				t.Errorf("Slug(%q) = %q, want %q", tc.branch, got, tc.want)
			}
		})
	}
}

func TestBranchDB(t *testing.T) {
	for _, tc := range []struct {
		base, branch, want, code string
	}{
		{"app", "main", "app__main", ""},
		{"app", strings.Repeat("a", 58), "app__" + strings.Repeat("a", 58), ""},
		{"app", strings.Repeat("a", 100), "app__" + strings.Repeat("a", 49) + "_28165978", ""},
		{strings.Repeat("b", 40), strings.Repeat("a", 100), strings.Repeat("b", 40) + "__" + strings.Repeat("a", 12) + "_28165978", ""},
		{"app", "", "", config.Usage},
		{"app", "a\xffb", "", config.Usage},
		{"app", "a\x00b", "", config.Usage},
		{"App", "main", "", config.ConfigInvalid},
		{"1app", "main", "", config.ConfigInvalid},
		{"app__nested", "main", "", config.ConfigInvalid},
		{strings.Repeat("b", 41), "main", "", config.ConfigInvalid},
	} {
		got, err := BranchDB(tc.base, tc.branch)
		if tc.code != "" {
			if config.ErrorCode(err) != tc.code {
				t.Errorf("BranchDB(%q, %q) error = %v, want %s", tc.base, tc.branch, err, tc.code)
			}
		} else if err != nil || got != tc.want || len(got) > 63 {
			t.Errorf("BranchDB(%q, %q) = %q, %v, want %q", tc.base, tc.branch, got, err, tc.want)
		}
	}
}

func TestGeneratedNames(t *testing.T) {
	for _, tc := range []struct {
		tag  string
		make func(string) (string, error)
	}{
		{"pool", PoolDB},
		{"tmp", TmpDB},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			db, err := tc.make("app")
			if err != nil || !regexp.MustCompile(`^app__`+tc.tag+`_[0-9a-f]{8}$`).MatchString(db) {
				t.Fatalf("generated name = %q, %v", db, err)
			}
			if !IsReservedName("app", db) || IsReservedName("ap", db) || IsReservedName("app", "app") {
				t.Fatalf("unexpected reserved name classification for %q", db)
			}
			if _, err := tc.make(strings.Repeat("a", 64)); config.ErrorCode(err) != config.ConfigInvalid {
				t.Errorf("oversized base error = %v", err)
			}
		})
	}
}
