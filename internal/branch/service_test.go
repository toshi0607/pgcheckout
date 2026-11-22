package branch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/toshi0607/pgtwig/internal/config"
)

func TestDatabaseError(t *testing.T) {
	for _, tc := range []struct{ state, code string }{
		{"XX000", config.Internal},
		{"42P04", config.DatabaseExists},
		{"53100", config.ServerResources},
		{"53300", config.ServerResources},
		{"57014", config.Canceled},
	} {
		err := databaseError(fmt.Errorf("copy: %w", &pgconn.PgError{Code: tc.state, Message: "server message"}), "")
		if config.ErrorCode(err) != tc.code || err.Error() != "server message (SQLSTATE "+tc.state+")" {
			t.Errorf("SQLSTATE %s mapped to %s, %v", tc.state, config.ErrorCode(err), err)
		}
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		err := databaseError(fmt.Errorf("copy: %w", cause), "")
		if config.ErrorCode(err) != config.Canceled || !errors.Is(err, cause) {
			t.Errorf("context error %v mapped to %s, %v", cause, config.ErrorCode(err), err)
		}
	}
}

func TestConnectErrorRedactsDSN(t *testing.T) {
	u, err := config.ParseAdminURL("postgres://alice:example-secret@localhost/app")
	if err != nil {
		t.Fatal(err)
	}
	svc := Service{
		Config: config.Config{AdminURL: u},
		Connect: func(_ context.Context, url string) (*pgx.Conn, error) {
			return nil, fmt.Errorf("invalid URL %s and password='other secret'", url)
		},
	}
	_, err = svc.Doctor(t.Context())
	if config.ErrorCode(err) != config.ConnectFailed ||
		!strings.Contains(err.Error(), "invalid URL <redacted-url> and password=<redacted>") ||
		strings.Contains(err.Error(), "secret") {
		t.Fatalf("unexpected connect failure: %v", err)
	}
}

func TestCleanupWarningPreservesSuccess(t *testing.T) {
	var warnings []string
	cleanupErr := errors.New("release claim lock: connection closed")
	if err := recordCleanup(nil, cleanupErr, &warnings); err != nil ||
		len(warnings) != 1 || warnings[0] != cleanupErr.Error() {
		t.Fatalf("successful action with failed cleanup: warnings=%v, err=%v", warnings, err)
	}
	cause := errors.New("main action failed")
	if err := recordCleanup(cause, cleanupErr, &warnings); !errors.Is(err, cause) || !errors.Is(err, cleanupErr) || len(warnings) != 1 {
		t.Fatalf("failed action with failed cleanup: warnings=%v, err=%v", warnings, err)
	}
}
