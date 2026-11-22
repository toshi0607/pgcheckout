package marker

import (
	"bytes"
	"encoding/json"
	"time"
)

const (
	Base   = "base"
	Branch = "branch"
	Pool   = "pool"
	Tmp    = "tmp"
)

type Marker struct {
	PGCheckout int       `json:"pgcheckout"`
	Kind       string    `json:"kind"`
	Base       string    `json:"base"`
	Gen        int       `json:"gen"`
	Branch     string    `json:"branch,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at,omitzero"`
	Strategy   string    `json:"strategy,omitempty"`
	Warnings   int       `json:"warnings,omitzero"`
	Open       *bool     `json:"open,omitempty"`
}

func New(kind, base string, gen int) Marker {
	m := Marker{PGCheckout: 1, Kind: kind, Base: base, Gen: gen, CreatedAt: time.Now().UTC()}
	if kind == Base {
		m.Open = new(false)
	}
	return m
}

func (m Marker) IsOpen() bool { return m.Open != nil && *m.Open }

func (m Marker) ValidFor(base string) bool {
	if m.PGCheckout != 1 || m.Base != base {
		return false
	}
	switch m.Kind {
	case Branch:
		return m.Branch != ""
	case Base, Pool, Tmp:
		return true
	default:
		return false
	}
}

func Parse(comment string) (Marker, bool) {
	data := []byte(comment)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || !bytes.Equal(fields["pgcheckout"], []byte("1")) {
		return Marker{}, false
	}
	m := Marker{PGCheckout: 1}
	for _, field := range []struct {
		name   string
		target any
	}{
		{"kind", &m.Kind}, {"base", &m.Base}, {"gen", &m.Gen}, {"branch", &m.Branch},
		{"created_at", &m.CreatedAt}, {"expires_at", &m.ExpiresAt},
		{"strategy", &m.Strategy}, {"warnings", &m.Warnings}, {"open", &m.Open},
	} {
		if raw, ok := fields[field.name]; ok {
			if err := json.Unmarshal(raw, field.target); err != nil {
				return Marker{}, false
			}
		}
	}
	return m, true
}
