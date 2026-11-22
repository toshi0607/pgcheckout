package branch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/toshi0607/pgtwig/internal/config"
	"github.com/toshi0607/pgtwig/internal/marker"
	"github.com/toshi0607/pgtwig/internal/naming"
	"github.com/toshi0607/pgtwig/internal/pg"
)

type copyResult struct {
	database pg.Database
	elapsed  int64
	warnings []string
}

func (s *session) copyFromBase(ctx context.Context, source pg.Database, gen int, strategy string) (copyResult, error) {
	start := time.Now()
	name, err := naming.TmpDB(s.Base)
	if err != nil {
		return copyResult{}, err
	}
	strategy, err = pg.CreateCopy(ctx, s.Conn, name, source.Name, strategy)
	if err != nil {
		return copyResult{}, err
	}
	m := marker.New(marker.Tmp, s.Base, gen)
	m.Strategy = strategy
	if err := pg.Comment(ctx, s.Conn, name, m); err != nil {
		return copyResult{}, &config.Error{
			Code:    config.ErrorCode(databaseError(err, "")),
			Message: fmt.Sprintf("could not mark newly created temporary database %q", name),
			Cause:   err,
		}
	}
	d := pg.Database{Name: name, Marker: m}
	actual, found, err := pg.Lookup(ctx, s.Conn, name)
	if err != nil {
		return copyResult{}, s.discardCopy(ctx, d, err)
	}
	if !found {
		return copyResult{}, errDatabaseGone
	}
	if actual.Marker != m {
		return copyResult{}, s.discardCopy(ctx, d, unmanaged())
	}
	warnings := pg.CopySettings(ctx, s.Conn, source, name)
	warnings = append(warnings, pg.CopyACL(ctx, s.Conn, source, name, actual.Owner)...)
	if err := ctx.Err(); err != nil {
		return copyResult{}, s.discardCopy(ctx, actual, fmt.Errorf("copy database: %w", err))
	}
	return copyResult{database: actual, elapsed: time.Since(start).Milliseconds(), warnings: warnings}, nil
}

func databaseGone(err error) bool {
	return errors.Is(err, errDatabaseGone) || pg.State(err) == "3D000"
}
