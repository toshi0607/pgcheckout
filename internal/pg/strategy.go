package pg

import (
	"context"

	"github.com/toshi0607/pgcheckout/internal/config"
)

const (
	Clone    = "file_copy_clone"
	FileCopy = "file_copy"
	WALLog   = "wal_log"
	Default  = "default"
)

func SelectStrategy(version int, configured string) (string, error) {
	switch configured {
	case "", "auto":
		switch {
		case version >= 180000:
			return Clone, nil
		case version >= 150000:
			return WALLog, nil
		default:
			return Default, nil
		}
	case "clone":
		if version >= 180000 {
			return Clone, nil
		}
	case FileCopy, WALLog:
		if version >= 150000 {
			return configured, nil
		}
	case Default:
		return Default, nil
	default:
		return "", config.NewError(config.ConfigInvalid, "unknown copy strategy", "")
	}
	return "", config.NewError(config.ConfigInvalid, "configured copy strategy is unavailable on this PostgreSQL version", "Use --strategy auto.")
}

func CreateCopy(ctx context.Context, db DB, name, template, strategy string) (string, error) {
	clause := ""
	switch strategy {
	case Clone:
		if err := Exec(ctx, db, "set file_copy_method", "SET file_copy_method = clone"); err != nil {
			strategy = FileCopy
		}
		clause = " STRATEGY = FILE_COPY"
	case FileCopy:
		clause = " STRATEGY = FILE_COPY"
	case WALLog:
		clause = " STRATEGY = WAL_LOG"
	}
	err := Exec(ctx, db, "copy database", "CREATE DATABASE "+Identifier(name)+" TEMPLATE "+Identifier(template)+clause)
	return strategy, err
}
