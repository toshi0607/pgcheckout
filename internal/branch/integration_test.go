//go:build integration

package branch

import (
	"context"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/toshi0607/pgtwig/internal/config"
	"github.com/toshi0607/pgtwig/internal/marker"
	"github.com/toshi0607/pgtwig/internal/naming"
	"github.com/toshi0607/pgtwig/internal/pg"
)

type fixture struct {
	svc    Service
	admin  *pgx.Conn
	source string
	role   string
}

func newFixture(t *testing.T, prepare func(*testing.T, *fixture)) *fixture {
	t.Helper()
	rawURL := os.Getenv("PGTWIG_TEST_ADMIN_URL")
	if rawURL == "" {
		t.Skip("PGTWIG_TEST_ADMIN_URL is not set")
	}
	t.Parallel()
	u, err := config.ParseAdminURL(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	random, err := naming.TmpDB("pt")
	if err != nil {
		t.Fatal(err)
	}
	base := "pgtwig_test_" + strings.TrimPrefix(random, "pt__tmp_")
	f := &fixture{
		svc:    Service{Config: config.Config{AdminURL: u, Base: base, PoolSize: 2, Strategy: "auto"}},
		source: base + "_source",
		admin:  connectTestDB(t, rawURL),
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		dbs, err := pg.Databases(ctx, f.admin)
		if err != nil {
			t.Errorf("list test databases for cleanup: %v", err)
			return
		}
		for _, d := range dbs {
			if d.Name != f.source && d.Name != base && !naming.IsReservedName(base, d.Name) {
				continue
			}
			if _, err := f.admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pg.Identifier(d.Name)); err != nil {
				t.Errorf("clean up test database %s: %v", d.Name, err)
			}
		}
		if f.role != "" {
			if _, err := f.admin.Exec(ctx, "DROP ROLE "+pg.Identifier(f.role)); err != nil {
				t.Errorf("clean up test role: %v", err)
			}
		}
	})
	execTestSQL(t, f.admin, "CREATE DATABASE "+pg.Identifier(f.source)+" TEMPLATE template0")
	if prepare != nil {
		prepare(t, f)
	}
	result, err := f.svc.BaseCreate(t.Context(), base, f.source, false)
	if err != nil {
		t.Fatalf("create base: %v", err)
	}
	if result.Gen != 1 || result.Open || result.AllowConnections || len(result.Warnings) > 0 {
		t.Fatalf("invalid new base result: %+v", result)
	}
	return f
}

func connectTestDB(t *testing.T, url string) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), url)
	if err != nil {
		t.Fatal("could not connect to the PostgreSQL test database")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := conn.Close(ctx); err != nil {
			t.Error("close PostgreSQL test connection:", err)
		}
	})
	return conn
}

func execTestSQL(t *testing.T, conn *pgx.Conn, sql string) {
	t.Helper()
	if _, err := conn.Exec(t.Context(), sql); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) ensure(t *testing.T, name string) EnsureResult {
	t.Helper()
	r, err := f.svc.Ensure(t.Context(), name, EnsureOptions{NoRefill: true})
	if err != nil {
		t.Fatalf("ensure %s: %v", name, err)
	}
	return r
}

func (f *fixture) fill(t *testing.T, size int) {
	t.Helper()
	r, err := f.svc.PoolFill(t.Context(), size)
	if err != nil || r.Created != size || r.PoolSize != size {
		t.Fatalf("fill %d: %+v, %v", size, r, err)
	}
}

func (f *fixture) exists(t *testing.T, name string) bool {
	t.Helper()
	_, found, err := pg.Lookup(t.Context(), f.admin, name)
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func (f *fixture) database(t *testing.T, name string) pg.Database {
	t.Helper()
	d, found, err := pg.Lookup(t.Context(), f.admin, name)
	if err != nil || !found {
		t.Fatalf("look up %s: found=%t, err=%v", name, found, err)
	}
	return d
}

func (f *fixture) expire(t *testing.T, names ...string) {
	t.Helper()
	var now time.Time
	if err := f.admin.QueryRow(t.Context(), "SELECT now()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		m := f.database(t, name).Marker
		m.ExpiresAt = now.Add(-time.Hour)
		if err := pg.Comment(t.Context(), f.admin, name, m); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEnsureCreatesAndIsIdempotent(t *testing.T) {
	f := newFixture(t, nil)
	first := f.ensure(t, "feat/login")
	second := f.ensure(t, "feat/login")
	if !first.Created || second.Created || first.Database != second.Database || first.URL != second.URL ||
		first.Source != "copy" || second.Source != "existing" || second.Stale || second.Gen != 1 {
		t.Fatal("ensure did not return the same current-generation branch on the second call")
	}
}

func TestCopyKeepsDataAndIsIsolated(t *testing.T) {
	f := newFixture(t, func(t *testing.T, f *fixture) {
		source := connectTestDB(t, f.svc.Config.DatabaseURL(f.source))
		execTestSQL(t, source, "CREATE TABLE public.items (id integer PRIMARY KEY)")
		execTestSQL(t, source, "INSERT INTO public.items VALUES (1)")
		if err := source.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
	first := connectTestDB(t, f.ensure(t, "first").URL)
	execTestSQL(t, first, "INSERT INTO public.items VALUES (2)")
	second := connectTestDB(t, f.ensure(t, "second").URL)
	var ids []int32
	if err := second.QueryRow(t.Context(), "SELECT array_agg(id ORDER BY id) FROM public.items").Scan(&ids); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []int32{1}) {
		t.Fatalf("second branch row IDs = %v, want [1]", ids)
	}
}

func TestCopyReplicatesSettingsAndACL(t *testing.T) {
	f := newFixture(t, func(t *testing.T, f *fixture) {
		name := pg.Identifier(f.source)
		execTestSQL(t, f.admin, "ALTER DATABASE "+name+" SET search_path TO twig_settings, public")
		execTestSQL(t, f.admin, "ALTER DATABASE "+name+" SET statement_timeout TO '250ms'")
		execTestSQL(t, f.admin, "REVOKE CONNECT, TEMPORARY ON DATABASE "+name+" FROM PUBLIC")
		role := f.svc.Config.Base + " guest"
		if _, err := f.admin.Exec(t.Context(), "CREATE ROLE "+pg.Identifier(role)); err != nil {
			if pg.State(err) != "42501" {
				t.Fatal(err)
			}
			t.Log("skipping named-role grant assertion because the test role lacks CREATEROLE")
			return
		}
		f.role = role
		execTestSQL(t, f.admin, "GRANT CONNECT ON DATABASE "+name+" TO "+pg.Identifier(role)+" WITH GRANT OPTION")
	})
	r := f.ensure(t, "settings")
	if len(r.Warnings) != 0 {
		t.Fatalf("copy warnings: %v", r.Warnings)
	}
	target := connectTestDB(t, r.URL)
	var path, timeout string
	err := target.QueryRow(t.Context(), "SELECT current_setting('search_path'), current_setting('statement_timeout')").Scan(&path, &timeout)
	if err != nil || path != "twig_settings, public" || timeout != "250ms" {
		t.Fatalf("branch settings = %q, %q, %v", path, timeout, err)
	}
	var publicGrants int
	err = f.admin.QueryRow(t.Context(), `SELECT count(*) FROM pg_database d, aclexplode(d.datacl) a
		WHERE d.datname = $1 AND a.grantee = 0 AND a.privilege_type IN ('CONNECT', 'TEMPORARY')`, r.Database).Scan(&publicGrants)
	if err != nil || publicGrants != 0 {
		t.Fatalf("PUBLIC has %d unexpected grants, query error %v", publicGrants, err)
	}
	if f.role != "" {
		var grantOption bool
		err := f.admin.QueryRow(t.Context(), "SELECT has_database_privilege($1::name, $2::text, 'CONNECT WITH GRANT OPTION')", f.role, r.Database).Scan(&grantOption)
		if err != nil || !grantOption {
			t.Fatalf("expected grantable CONNECT for copied role, got %v, %v", grantOption, err)
		}
	}
}

func TestEnsureUsesPoolThenCopy(t *testing.T) {
	f := newFixture(t, nil)
	f.fill(t, 1)
	first := f.ensure(t, "first")
	second := f.ensure(t, "second")
	if first.Source != "pool" || second.Source != "copy" || first.Database == second.Database {
		t.Fatalf("expected pool then copy, got %s then %s", first.Source, second.Source)
	}
}

func TestConcurrentEnsureSameBranch(t *testing.T) {
	f := newFixture(t, nil)
	stats := f.concurrentEnsure(t, []string{"same", "same", "same", "same", "same", "same", "same", "same"})
	created := 0
	for _, result := range stats {
		if result.Created {
			created++
		}
		if result.Database != stats[0].Database {
			t.Fatal("concurrent ensure returned different databases")
		}
	}
	list, err := f.svc.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 || len(list.Branches) != 1 || len(list.Tmp) != 0 {
		t.Fatalf("created=%d, branches=%d, temporary=%d, want 1, 1, 0", created, len(list.Branches), len(list.Tmp))
	}
}

func TestConcurrentEnsureDifferentBranchesOnePoolEntry(t *testing.T) {
	f := newFixture(t, nil)
	f.fill(t, 1)
	stats := f.concurrentEnsure(t, []string{"first", "second"})
	sources := []string{stats[0].Source, stats[1].Source}
	slices.Sort(sources)
	if !slices.Equal(sources, []string{"copy", "pool"}) || stats[0].Database == stats[1].Database ||
		!stats[0].Created || !stats[1].Created {
		t.Fatalf("expected two databases and one use of the pool, got sources %v", sources)
	}
}

func (f *fixture) concurrentEnsure(t *testing.T, names []string) []EnsureResult {
	t.Helper()
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]EnsureResult, len(names))
	errs := make([]error, len(names))
	for i, name := range names {
		wg.Go(func() {
			<-start
			results[i], errs[i] = f.svc.Ensure(t.Context(), name, EnsureOptions{NoRefill: true})
		})
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	return results
}

func TestDropRefusesUnmanaged(t *testing.T) {
	f := newFixture(t, nil)
	name, err := naming.BranchDB(f.svc.Config.Base, "foreign")
	if err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, f.admin, "CREATE DATABASE "+pg.Identifier(name)+" TEMPLATE template0")
	if _, err := f.svc.Drop(t.Context(), "foreign", false); config.ErrorCode(err) != config.UnmanagedDatabase {
		t.Fatalf("drop unmanaged error = %v", err)
	}
	if !f.exists(t, name) {
		t.Fatal("unmanaged database was dropped")
	}
}

func TestDropNeverDropsBase(t *testing.T) {
	f := newFixture(t, nil)
	_, err := run(t.Context(), f.svc, func(s *session) (struct{}, error) {
		base, err := s.lookupBase(t.Context())
		if err != nil {
			return struct{}{}, err
		}
		if err := s.dropManaged(t.Context(), base, false); config.ErrorCode(err) != config.UnmanagedDatabase {
			t.Fatalf("drop base marker error = %v", err)
		}
		base.Marker.Kind = marker.Tmp
		return struct{}{}, s.dropManaged(t.Context(), base, false)
	})
	if config.ErrorCode(err) != config.UnmanagedDatabase {
		t.Fatalf("drop base name error = %v", err)
	}
	if !f.exists(t, f.svc.Config.Base) {
		t.Fatal("base database was dropped")
	}
}

func TestBaseOpenCloseBumpsGenAndDropsStalePool(t *testing.T) {
	f := newFixture(t, nil)
	initial := f.ensure(t, "branch")
	f.fill(t, 1)
	status, err := f.svc.PoolStatus(t.Context())
	if err != nil || status.Ready != 1 {
		t.Fatalf("status before close: %+v, %v", status, err)
	}
	opened, err := f.svc.BaseOpen(t.Context())
	if err != nil || !opened.Changed || !opened.Open || !opened.AllowConnections || opened.Gen != 1 {
		t.Fatalf("open base: %+v, %v", opened, err)
	}
	closed, err := f.svc.BaseClose(t.Context(), false)
	if err != nil || !closed.Changed || closed.Open || closed.AllowConnections || closed.Gen != 2 {
		t.Fatalf("close base: %+v, %v", closed, err)
	}
	if f.exists(t, status.Databases[0].Name) {
		t.Fatal("stale pool database survived base close")
	}
	again, err := f.svc.BaseClose(t.Context(), false)
	if err != nil || again.Changed || again.Gen != 2 {
		t.Fatalf("close already closed base: %+v, %v", again, err)
	}
	stale := f.ensure(t, "branch")
	if !stale.Stale || stale.Created || stale.Source != "existing" || stale.Gen != 1 ||
		stale.BaseGen != 2 || stale.Database != initial.Database {
		t.Fatal("ensure did not preserve the stale branch after closing the base")
	}
}

func TestEnsureFailsWhileBaseOpen(t *testing.T) {
	f := newFixture(t, nil)
	if _, err := f.svc.BaseOpen(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Ensure(t.Context(), "branch", EnsureOptions{NoRefill: true}); config.ErrorCode(err) != config.BaseOpen {
		t.Fatalf("ensure against open base error = %v", err)
	}
}

func TestGCDropsExpiredOnly(t *testing.T) {
	f := newFixture(t, nil)
	expired := f.ensure(t, "expired")
	held := f.ensure(t, "expired_in_use")
	f.expire(t, expired.Database, held.Database)
	future, err := f.svc.Ensure(t.Context(), "future", EnsureOptions{TTL: new(time.Hour), NoRefill: true})
	if err != nil {
		t.Fatal(err)
	}
	permanent := f.ensure(t, "permanent")
	preview, err := f.svc.GC(t.Context(), true)
	if err != nil || !preview.DryRun || !slices.Contains(preview.Dropped, expired.Database) || !f.exists(t, expired.Database) {
		t.Fatalf("dry-run GC: %+v, %v", preview, err)
	}
	connectTestDB(t, held.URL)
	actual, err := f.svc.GC(t.Context(), false)
	if err != nil || actual.DryRun || !slices.Equal(actual.Dropped, []string{expired.Database}) ||
		!slices.Equal(actual.Skipped, []Skipped{{Database: held.Database, Reason: config.DatabaseInUse}}) {
		t.Fatalf("actual GC: %+v, %v", actual, err)
	}
	if f.exists(t, expired.Database) || !f.exists(t, future.Database) || !f.exists(t, permanent.Database) || !f.exists(t, held.Database) {
		t.Fatal("GC removed the wrong database")
	}
}

func TestDropInUseNeedsForce(t *testing.T) {
	f := newFixture(t, nil)
	r := f.ensure(t, "in_use")
	connectTestDB(t, r.URL)
	if _, err := f.svc.Drop(t.Context(), "in_use", false); config.ErrorCode(err) != config.DatabaseInUse {
		t.Fatalf("drop in-use branch error = %v", err)
	}
	if dropped, err := f.svc.Drop(t.Context(), "in_use", true); err != nil || !dropped.Dropped || f.exists(t, r.Database) {
		t.Fatalf("forced drop: %+v, %v", dropped, err)
	}
}

func TestAdoptRefusesOpenBase(t *testing.T) {
	f := newFixture(t, nil)
	name := f.svc.Config.Base
	opened, err := f.svc.BaseOpen(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.BaseAdopt(t.Context(), name, false); config.ErrorCode(err) != config.BaseOpen {
		t.Fatalf("adopt open base error = %v, want base_open", err)
	}
	d := f.database(t, name)
	if !d.AllowConnections || !d.Marker.IsOpen() || d.Marker.Gen != opened.Gen {
		t.Fatalf("adopt changed open base: %+v", d)
	}
}

func TestCloseRevertsWhenSessionSurvives(t *testing.T) {
	f := newFixture(t, nil)
	name := f.svc.Config.Base
	opened, err := f.svc.BaseOpen(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	held := connectTestDB(t, f.svc.Config.DatabaseURL(name))
	if _, err := f.svc.BaseClose(t.Context(), false); config.ErrorCode(err) != config.DatabaseInUse {
		t.Fatalf("close with an active session = %v, want database_in_use", err)
	}
	d := f.database(t, name)
	if !d.AllowConnections || !d.Marker.IsOpen() || d.Marker.Gen != opened.Gen {
		t.Fatalf("unsuccessful close changed base state: %+v", d)
	}
	if err := held.Ping(t.Context()); err != nil {
		t.Fatalf("close without --terminate disconnected the active session: %v", err)
	}
	closed, err := f.svc.BaseClose(t.Context(), true)
	if err != nil || closed.Open || closed.AllowConnections || closed.Gen != opened.Gen+1 {
		t.Fatalf("close with termination: %+v, %v", closed, err)
	}
	if err := held.Ping(t.Context()); err == nil {
		t.Fatal("session survived close with termination")
	}
}

func TestCloseRepairsAllowedConnections(t *testing.T) {
	f := newFixture(t, nil)
	name := f.svc.Config.Base
	execTestSQL(t, f.admin, "ALTER DATABASE "+pg.Identifier(name)+" WITH ALLOW_CONNECTIONS true")
	if _, err := f.svc.BaseAdopt(t.Context(), name, false); config.ErrorCode(err) != config.BaseOpen {
		t.Fatalf("adopt base with connections enabled = %v, want base_open", err)
	}
	closed, err := f.svc.BaseClose(t.Context(), false)
	if err != nil || !closed.Changed || closed.Open || closed.AllowConnections || closed.Gen != 2 {
		t.Fatalf("close base with connections enabled: %+v, %v", closed, err)
	}
}

func TestEnsureExistingBranchWhileBaseOpen(t *testing.T) {
	f := newFixture(t, nil)
	first := f.ensure(t, "existing")
	if _, err := f.svc.BaseOpen(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.Ensure(t.Context(), "existing", EnsureOptions{})
	if err != nil || got.Created || got.Source != "existing" || got.Stale || got.BaseGen != first.Gen ||
		got.DSNResult != first.DSNResult || got.RefillNeeded {
		t.Fatalf("ensure existing with open base: source=%q created=%t gen=%d err=%v", got.Source, got.Created, got.BaseGen, err)
	}
	if _, err := f.svc.Ensure(t.Context(), "new", EnsureOptions{}); config.ErrorCode(err) != config.BaseOpen {
		t.Fatalf("ensure new branch with open base = %v, want base_open", err)
	}
}

func TestHostileBranchNames(t *testing.T) {
	f := newFixture(t, nil)
	for _, branch := range []string{"a'b", `a"b`, "a;b", `a\b`, "a b", strings.Repeat("a", 200)} {
		got := f.ensure(t, branch)
		dsn, err := f.svc.DSN(t.Context(), branch)
		if err != nil || dsn != got.DSNResult || dsn.Branch != branch {
			t.Fatalf("DSN did not match ensure for %q: %v", branch, err)
		}
		dropped, err := f.svc.Drop(t.Context(), branch, false)
		if err != nil || !dropped.Dropped || dropped.Branch != branch || dropped.Database != got.Database || f.exists(t, got.Database) {
			t.Fatalf("drop %q: %+v, %v", branch, dropped, err)
		}
	}
}

func TestTwoBasesAreIsolated(t *testing.T) {
	f := newFixture(t, nil)
	other := *f
	otherName := f.svc.Config.Base + "b"
	other.svc.Config.Base = otherName
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		dbs, err := pg.Databases(ctx, f.admin)
		if err != nil {
			t.Errorf("list test databases for cleanup: %v", err)
			return
		}
		for _, d := range dbs {
			if d.Name == otherName || naming.IsReservedName(otherName, d.Name) {
				if err := pg.Exec(ctx, f.admin, "clean up test database", "DROP DATABASE "+pg.Identifier(d.Name)); err != nil {
					t.Error(err)
				}
			}
		}
	})
	if _, err := other.svc.BaseCreate(t.Context(), otherName, f.source, false); err != nil {
		t.Fatal(err)
	}
	firstBranch := f.ensure(t, "topic")
	otherBranch := other.ensure(t, "topic")
	f.expire(t, firstBranch.Database, otherBranch.Database)
	f.fill(t, 1)
	other.fill(t, 1)
	status, err := other.svc.PoolStatus(t.Context())
	if err != nil || len(status.Databases) != 1 {
		t.Fatalf("other pool status: %+v, %v", status, err)
	}
	otherPool := status.Databases[0].Name
	for _, action := range []struct {
		name string
		run  func() error
	}{
		{"pool drain", func() error { _, err := f.svc.PoolDrain(t.Context()); return err }},
		{"gc", func() error { _, err := f.svc.GC(t.Context(), false); return err }},
		{"base close", func() error {
			f.fill(t, 1)
			if _, err := f.svc.BaseOpen(t.Context()); err != nil {
				return err
			}
			_, err := f.svc.BaseClose(t.Context(), false)
			return err
		}},
	} {
		if err := action.run(); err != nil {
			t.Fatalf("%s: %v", action.name, err)
		}
		for _, name := range []string{otherName, otherBranch.Database, otherPool} {
			if !f.exists(t, name) {
				t.Fatalf("%s removed other base's database %q", action.name, name)
			}
		}
	}
	list, err := f.svc.List(t.Context())
	if err != nil || len(list.Unmanaged) != 0 || len(list.Pool) != 0 || len(list.Branches) != 0 {
		t.Fatalf("list after isolated cleanup: %+v, %v", list, err)
	}
}

func TestGCAndDrainSkipForgedAndForeign(t *testing.T) {
	f := newFixture(t, nil)
	base := f.svc.Config.Base
	forged, foreign := base+"__pool_00000001", base+"__pool_00000002"
	for _, name := range []string{forged, foreign} {
		execTestSQL(t, f.admin, "CREATE DATABASE "+pg.Identifier(name)+" TEMPLATE template0")
	}
	execTestSQL(t, f.admin, "COMMENT ON DATABASE "+pg.Identifier(foreign)+
		` IS '{"pgtwig":1,"kind":"pool","base":"another_base","gen":0}'`)
	if _, err := f.svc.GC(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PoolDrain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.BaseOpen(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.BaseClose(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if !f.exists(t, forged) || !f.exists(t, foreign) {
		t.Fatal("a database without a matching marker was dropped")
	}
	list, err := f.svc.List(t.Context())
	if err != nil || !slices.Equal(list.Unmanaged, []string{forged}) {
		t.Fatalf("list unmanaged: %v, %v", list.Unmanaged, err)
	}
	doctor, err := f.svc.Doctor(t.Context())
	if err != nil || !slices.Equal(doctor.Unmanaged, []string{forged}) ||
		!strings.Contains(doctor.Note, "("+f.svc.Config.MaintenanceDB()+")") {
		t.Fatalf("doctor unmanaged: %v, note: %q, err: %v", doctor.Unmanaged, doctor.Note, err)
	}
}

func TestResetRecreatesFromCurrentBase(t *testing.T) {
	f := newFixture(t, nil)
	first := f.ensure(t, "topic")
	initial := f.database(t, first.Database)
	if _, err := f.svc.BaseOpen(t.Context()); err != nil {
		t.Fatal(err)
	}
	base := connectTestDB(t, f.svc.Config.DatabaseURL(f.svc.Config.Base))
	execTestSQL(t, base, "CREATE TABLE public.reset_data AS SELECT 42 AS value")
	if err := base.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.BaseClose(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	reset, err := f.svc.Reset(t.Context(), "topic", false, EnsureOptions{NoRefill: true})
	if err != nil || !reset.Reset || !reset.Created || reset.Stale || reset.Gen != 2 || reset.Database != first.Database {
		t.Fatalf("reset did not recreate current-generation branch: %v", err)
	}
	if f.database(t, reset.Database).OID == initial.OID {
		t.Fatal("reset preserved the original database")
	}
	branch := connectTestDB(t, reset.URL)
	var value int
	if err := branch.QueryRow(t.Context(), "SELECT value FROM public.reset_data").Scan(&value); err != nil || value != 42 {
		t.Fatalf("reset branch data = %d, %v", value, err)
	}
}

func TestPoolDrain(t *testing.T) {
	f := newFixture(t, nil)
	branch := f.ensure(t, "topic")
	f.fill(t, 2)
	list, err := f.svc.List(t.Context())
	if err != nil || len(list.Pool) != 2 {
		t.Fatalf("pool before drain: %+v, %v", list.Pool, err)
	}
	drained, err := f.svc.PoolDrain(t.Context())
	if err != nil || len(drained.Dropped) != 2 || len(drained.Skipped) != 0 || !f.exists(t, branch.Database) {
		t.Fatalf("drain: %+v, %v", drained, err)
	}
	for _, d := range list.Pool {
		if !slices.Contains(drained.Dropped, d.Name) || f.exists(t, d.Name) {
			t.Fatalf("drain missed pool database %q", d.Name)
		}
	}
}

func TestBaseAdoptIdempotent(t *testing.T) {
	f := newFixture(t, nil)
	before := f.database(t, f.source)
	first, err := f.svc.BaseAdopt(t.Context(), f.source, false)
	if err != nil || !first.Changed || first.Open || first.AllowConnections || first.Gen != 1 {
		t.Fatalf("first adopt: %+v, %v", first, err)
	}
	second, err := f.svc.BaseAdopt(t.Context(), f.source, false)
	if err != nil || second.Changed || second.BaseInfo != first.BaseInfo {
		t.Fatalf("second adopt: %+v, %v", second, err)
	}
	after := f.database(t, f.source)
	if after.OID != before.OID || after.AllowConnections || after.Marker.IsOpen() || !after.Marker.ValidFor(f.source) {
		t.Fatalf("adopt did not preserve the original database: %+v", after)
	}
}

func TestPoolClaimReportsReplicationWarnings(t *testing.T) {
	f := newFixture(t, nil)
	execTestSQL(t, f.admin, "ALTER DATABASE "+pg.Identifier(f.svc.Config.Base)+` SET search_path TO "schema/with/slashes"`)
	filled, err := f.svc.PoolFill(t.Context(), 1)
	if err != nil || filled.Created != 1 || filled.Discarded != 0 || len(filled.Warnings) != 1 {
		t.Fatalf("fill with a setting that cannot be replicated: %+v, %v", filled, err)
	}
	claimed := f.ensure(t, "warned")
	const warning = "1 per-database settings or grants could not be replicated from the base when this copy was made; run pgtwig doctor"
	if claimed.Source != "pool" || !slices.Equal(claimed.Warnings, []string{warning}) {
		t.Fatalf("claim: source=%q, warnings=%v", claimed.Source, claimed.Warnings)
	}
}
