package pg

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

var (
	settingName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
	listValue   = regexp.MustCompile(`^[A-Za-z0-9_ ,."$-]*$`)
)

func CopySettings(ctx context.Context, db DB, source Database, target string) []string {
	rows, err := db.Query(ctx, `SELECT unnest(setconfig) FROM pg_db_role_setting WHERE setdatabase = $1 AND setrole = 0`, source.OID)
	if err != nil {
		return []string{"could not read source database settings"}
	}
	settings, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return []string{"could not read source database settings"}
	}

	var warnings []string
	for _, setting := range settings {
		name, value, ok := strings.Cut(setting, "=")
		if !ok || !settingName.MatchString(name) {
			warnings = append(warnings, "skipped a setting with an invalid name")
			continue
		}
		literal, err := settingValue(name, value)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("skipped database setting %s: %s", name, err))
			continue
		}
		identifier := pgx.Identifier(strings.Split(strings.ToLower(name), ".")).Sanitize()
		err = Exec(ctx, db, "set database setting", "ALTER DATABASE "+Identifier(target)+" SET "+identifier+" TO "+literal)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("could not apply database setting %s", name))
		}
	}
	return warnings
}

func settingValue(name, value string) (string, error) {
	switch strings.ToLower(name) {
	case "search_path", "session_preload_libraries", "local_preload_libraries", "temp_tablespaces", "datestyle":
		if !listValue.MatchString(value) {
			return "", fmt.Errorf("invalid list value")
		}
		if value == "" {
			return quoteLiteral(value)
		}
		return value, nil
	default:
		return quoteLiteral(value)
	}
}
