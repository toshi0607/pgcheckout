package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type privilege struct {
	role      string
	name      string
	public    bool
	grantable bool
}

func CopyACL(ctx context.Context, db DB, source Database, target, owner string) []string {
	if !source.HasACL {
		return nil
	}
	rows, err := db.Query(ctx, `SELECT CASE WHEN a.grantee = 0 THEN '' ELSE pg_get_userbyid(a.grantee) END,
		a.privilege_type, a.grantee = 0, a.is_grantable
		FROM pg_database AS d, aclexplode(d.datacl) AS a WHERE d.oid = $1`, source.OID)
	if err != nil {
		return []string{"could not read source database privileges"}
	}
	privileges, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (privilege, error) {
		var p privilege
		err := row.Scan(&p.role, &p.name, &p.public, &p.grantable)
		return p, err
	})
	if err != nil {
		return []string{"could not read source database privileges"}
	}

	var warnings []string
	public := make(map[string]bool)
	for _, p := range privileges {
		if !p.public && p.role == owner {
			continue
		}
		switch p.name {
		case "CONNECT", "TEMPORARY", "CREATE":
		default:
			warnings = append(warnings, "skipped an unknown database privilege")
			continue
		}
		role := "PUBLIC"
		if p.public {
			public[p.name] = true
		} else {
			role = Identifier(p.role)
		}
		sql := "GRANT " + p.name + " ON DATABASE " + Identifier(target) + " TO " + role
		if p.grantable {
			sql += " WITH GRANT OPTION"
		}
		if err := Exec(ctx, db, "grant database privilege", sql); err != nil {
			warnings = append(warnings, fmt.Sprintf("could not grant %s to %s", p.name, role))
		}
	}
	for _, privilege := range []string{"CONNECT", "TEMPORARY"} {
		if public[privilege] {
			continue
		}
		sql := "REVOKE " + privilege + " ON DATABASE " + Identifier(target) + " FROM PUBLIC"
		if err := Exec(ctx, db, "revoke default database privilege", sql); err != nil {
			warnings = append(warnings, fmt.Sprintf("could not revoke %s from PUBLIC", privilege))
		}
	}
	return warnings
}
