package marker

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	created := time.Date(2026, 11, 22, 6, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		m       Marker
		omit    []string
		include []string
	}{
		{
			Marker{PGTwig: 1, Kind: Base, Base: "app", Gen: 1, CreatedAt: created, Open: new(false)},
			[]string{"branch", "strategy", "expires_at"}, []string{`"open":false`},
		},
		{
			Marker{PGTwig: 1, Kind: Branch, Base: "app", Gen: 2, Branch: "feat/login",
				CreatedAt: created, ExpiresAt: created.Add(time.Hour), Strategy: "wal_log"},
			[]string{"open"}, []string{`"expires_at":"2026-11-22T07:00:00Z"`, `"branch":"feat/login"`},
		},
		{
			Marker{PGTwig: 1, Kind: Pool, Base: "app", Gen: 2, CreatedAt: created, Strategy: "wal_log"},
			[]string{"branch", "open", "expires_at"}, []string{`"strategy":"wal_log"`},
		},
		{
			Marker{PGTwig: 1, Kind: Tmp, Base: "app", Gen: 2, CreatedAt: created, Strategy: "wal_log"},
			[]string{"branch", "open", "expires_at"}, []string{`"kind":"tmp"`},
		},
	} {
		t.Run(tc.m.Kind, func(t *testing.T) {
			data, err := json.Marshal(tc.m)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := Parse(string(data))
			if !ok || !reflect.DeepEqual(got, tc.m) {
				t.Fatalf("Parse(%s) = %+v, %v", data, got, ok)
			}
			for _, key := range tc.omit {
				if strings.Contains(string(data), `"`+key+`":`) {
					t.Errorf("unexpected key %s in %s", key, data)
				}
			}
			for _, field := range tc.include {
				if !strings.Contains(string(data), field) {
					t.Errorf("expected %s in %s", field, data)
				}
			}
		})
	}
}

func TestParseForeign(t *testing.T) {
	for _, comment := range []string{"", "regular comment", `{"pgtwig":`, `{"kind":"branch"}`, `{"pgtwig":2}`, `{"PGTWIG":1}`, `{"pgtwig":"1"}`, `null`, `[]`} {
		if _, ok := Parse(comment); ok {
			t.Errorf("Parse(%q) accepted a foreign comment", comment)
		}
	}
}

func TestMarkerFieldsAreCaseSensitive(t *testing.T) {
	m, ok := Parse(`{"pgtwig":1,"kind":"pool","base":"another","Base":"app"}`)
	if !ok || m.ValidFor("app") || !m.ValidFor("another") {
		t.Fatal("marker accepted a base from a differently cased key")
	}
}
