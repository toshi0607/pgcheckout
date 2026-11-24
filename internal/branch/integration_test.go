//go:build integration

package branch

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/toshi0607/pgcheckout/internal/config"
	"github.com/toshi0607/pgcheckout/internal/marker"
	"github.com/toshi0607/pgcheckout/internal/naming"
	"github.com/toshi0607/pgcheckout/internal/pg"
	"github.com/toshi0607/pgcheckout/internal/testpg"
)

type fixture struct {
	svc    Service
	admin  *pgx.Conn
	source string
	role   string
}

func newFixture(t *testing.T, prepare func(*testing.T, *fixture)) *fixture {
	t.Helper()
	rawURL := os.Getenv("PGCHECKOUT_TEST_ADMIN_URL")
	if rawURL == "" {
		t.Skip("PGCHECKOUT_TEST_ADMIN_URL is not set")
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
	base := "pgcheckout_test_" + strings.TrimPrefix(random, "pt__tmp_")
	f := &fixture{
		svc:    Service{Config: config.Config{AdminURL: u, Base: base, PoolSize: 2, Strategy: "auto"}},
		source: base + "_source",
		admin:  connectTestDB(t, rawURL),
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := testpg.CleanupDatabases(ctx, f.admin, base, f.source); err != nil {
			t.Error(err)
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

func (f *fixture) viaProxy(t *testing.T, hook func(string) testpg.QueryFault) (Service, *testpg.FaultProxy) {
	t.Helper()
	svc := f.svc
	adminURL, proxy := testpg.StartFaultProxy(t, svc.Config.AdminURL, hook)
	svc.Config.AdminURL = adminURL
	return svc, proxy
}

func (f *fixture) tmpDatabases(t *testing.T) []pg.Database {
	t.Helper()
	dbs, err := pg.Databases(t.Context(), f.admin)
	if err != nil {
		t.Fatal(err)
	}
	var tmp []pg.Database
	for _, d := range dbs {
		if strings.HasPrefix(d.Name, f.svc.Config.Base+"__tmp_") {
			tmp = append(tmp, d)
		}
	}
	return tmp
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

func TestCopyMarkerRetrySucceeds(t *testing.T) {
	f := newFixture(t, nil)
	prefix := `COMMENT ON DATABASE "` + f.svc.Config.Base + "__tmp_"
	var first atomic.Bool
	svc, proxy := f.viaProxy(t, func(sql string) testpg.QueryFault {
		if strings.HasPrefix(sql, prefix) && !first.Swap(true) {
			return testpg.InjectQueryError
		}
		return testpg.ForwardQuery
	})
	result, err := svc.Ensure(t.Context(), "retry_marker", EnsureOptions{NoRefill: true})
	if got := proxy.Injected(testpg.InjectQueryError); got != 1 {
		t.Fatalf("injected comment errors = %d, want 1 (ensure error: %v)", got, err)
	}
	if err != nil || result.Source != "copy" {
		t.Fatalf("ensure after marker retry: source=%q, err=%v", result.Source, err)
	}
	if tmp := f.tmpDatabases(t); len(tmp) != 0 {
		t.Fatalf("ensure left temporary databases: %+v", tmp)
	}
}

func TestCopyMarkerRetryExhausted(t *testing.T) {
	f := newFixture(t, nil)
	prefix := `COMMENT ON DATABASE "` + f.svc.Config.Base + "__tmp_"
	svc, proxy := f.viaProxy(t, func(sql string) testpg.QueryFault {
		if strings.HasPrefix(sql, prefix) {
			return testpg.InjectQueryError
		}
		return testpg.ForwardQuery
	})
	_, err := svc.Ensure(t.Context(), "exhaust_marker", EnsureOptions{NoRefill: true})
	if got := proxy.Injected(testpg.InjectQueryError); got != 3 {
		t.Fatalf("injected comment errors = %d, want 3 (ensure error: %v)", got, err)
	}
	tmp := f.tmpDatabases(t)
	if len(tmp) != 1 || tmp[0].Comment != nil {
		t.Fatalf("want one temporary database with a NULL comment, got %+v", tmp)
	}
	name := tmp[0].Name
	if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "must be dropped manually") {
		t.Fatalf("ensure error = %v, want manual cleanup for %q", err, name)
	}
	list, err := f.svc.List(t.Context())
	if err != nil || !slices.Equal(list.Unmanaged, []string{name}) {
		t.Fatalf("list unmanaged: %v, %v", list.Unmanaged, err)
	}
	doctor, err := f.svc.Doctor(t.Context())
	if err != nil || !slices.Equal(doctor.Unmanaged, []string{name}) {
		t.Fatalf("doctor unmanaged: %v, %v", doctor.Unmanaged, err)
	}
}

func TestCopyRecoveryMarksAndNamesLeftover(t *testing.T) {
	f := newFixture(t, nil)
	base := f.svc.Config.Base
	prefix := `CREATE DATABASE "` + base + "__tmp_"
	var first atomic.Bool
	svc, proxy := f.viaProxy(t, func(sql string) testpg.QueryFault {
		if strings.HasPrefix(sql, prefix) && !first.Swap(true) {
			return testpg.DropQueryReply
		}
		return testpg.ForwardQuery
	})
	_, err := svc.Ensure(t.Context(), "recover_marker", EnsureOptions{NoRefill: true})
	if got := proxy.Injected(testpg.DropQueryReply); got != 1 {
		t.Fatalf("dropped CREATE replies = %d, want 1 (ensure error: %v)", got, err)
	}
	tmp := f.tmpDatabases(t)
	if len(tmp) != 1 || !tmp[0].Marker.ValidFor(base) || tmp[0].Marker.Kind != marker.Tmp {
		t.Fatalf("want one managed temporary database, got %+v", tmp)
	}
	name := tmp[0].Name
	problem, ok := errors.AsType[*config.Error](err)
	if !ok {
		t.Fatalf("ensure error = %v, want *config.Error for %q", err, name)
	}
	if !strings.Contains(problem.Message, name) || !strings.Contains(problem.Message, "marked for cleanup") ||
		!strings.Contains(problem.Hint, "pool drain") {
		t.Fatalf("ensure message = %q, hint = %q, want marked database %q and a pool drain hint", problem.Message, problem.Hint, name)
	}
	drained, err := f.svc.PoolDrain(t.Context())
	if err != nil || !slices.Equal(drained.Dropped, []string{name}) {
		t.Fatalf("drain recovered copy: %+v, %v", drained, err)
	}
	if tmp := f.tmpDatabases(t); len(tmp) != 0 {
		t.Fatalf("pool drain left temporary databases: %+v", tmp)
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

func TestBaseRecreatedStartsAfterLeftoverGen(t *testing.T) {
	f := newFixture(t, func(t *testing.T, f *fixture) {
		source := connectTestDB(t, f.svc.Config.DatabaseURL(f.source))
		execTestSQL(t, source, "CREATE TABLE public.items (id integer PRIMARY KEY)")
		execTestSQL(t, source, "INSERT INTO public.items VALUES (1)")
		if err := source.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
	f.ensure(t, "b1")
	f.fill(t, 1)
	name := f.svc.Config.Base
	execTestSQL(t, f.admin, "DROP DATABASE "+pg.Identifier(name))
	source := connectTestDB(t, f.svc.Config.DatabaseURL(f.source))
	execTestSQL(t, source, "UPDATE public.items SET id = 2")
	if err := source.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	recreated, err := f.svc.BaseCreate(t.Context(), name, f.source, false)
	if err != nil || recreated.Gen != 2 {
		t.Fatalf("recreate base: %+v, %v", recreated, err)
	}
	if old := f.ensure(t, "b1"); old.Source != "existing" || !old.Stale {
		t.Fatalf("ensure b1: source=%q, stale=%t", old.Source, old.Stale)
	}
	fresh := f.ensure(t, "b2")
	if fresh.Source != "copy" || fresh.Stale {
		t.Fatalf("ensure b2: source=%q, stale=%t", fresh.Source, fresh.Stale)
	}
	branch := connectTestDB(t, fresh.URL)
	var ids []int32
	if err := branch.QueryRow(t.Context(), "SELECT array_agg(id ORDER BY id) FROM public.items").Scan(&ids); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ids, []int32{2}) {
		t.Fatalf("b2 row IDs = %v, want [2]", ids)
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
		` IS '{"pgcheckout":1,"kind":"pool","base":"another_base","gen":0}'`)
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

func TestBaseAdoptStartsAfterLeftoverGen(t *testing.T) {
	f := newFixture(t, nil)
	f.ensure(t, "b1")
	f.fill(t, 1)
	name := f.svc.Config.Base
	execTestSQL(t, f.admin, "DROP DATABASE "+pg.Identifier(name))
	execTestSQL(t, f.admin, "CREATE DATABASE "+pg.Identifier(name)+" TEMPLATE template0")
	adopted, err := f.svc.BaseAdopt(t.Context(), name, false)
	if err != nil || adopted.Gen != 2 {
		t.Fatalf("adopt replacement base: %+v, %v", adopted, err)
	}
	if old := f.ensure(t, "b1"); old.Source != "existing" || !old.Stale {
		t.Fatalf("ensure b1: source=%q, stale=%t", old.Source, old.Stale)
	}
}

func TestAdoptFailureRestoresDatabase(t *testing.T) {
	f := newFixture(t, nil)
	name := f.source
	execTestSQL(t, f.admin, "COMMENT ON DATABASE "+pg.Identifier(name)+" IS 'original'")
	held := connectTestDB(t, f.svc.Config.DatabaseURL(name))
	svc := f.svc
	svc.Config.Base = name
	if _, err := svc.BaseAdopt(t.Context(), name, false); config.ErrorCode(err) != config.DatabaseInUse {
		t.Fatalf("adopt with an active session = %v, want database_in_use", err)
	}
	d := f.database(t, name)
	if d.Comment == nil || *d.Comment != "original" || !d.AllowConnections || d.Marker.PGCheckout != 0 {
		t.Fatalf("failed adopt changed database state: %+v", d)
	}
	if err := held.Ping(t.Context()); err != nil {
		t.Fatalf("failed adopt disconnected the active session: %v", err)
	}
	if _, err := svc.List(t.Context()); config.ErrorCode(err) != config.BaseNotMarked {
		t.Fatalf("list after failed adopt = %v, want base_not_marked", err)
	}
	doctor, err := svc.Doctor(t.Context())
	if err != nil || doctor.Base != nil || !slices.ContainsFunc(doctor.Problems, func(problem config.Error) bool {
		return problem.Code == config.BaseNotMarked
	}) {
		t.Fatalf("doctor after failed adopt: %+v, %v", doctor, err)
	}
	adopted, err := svc.BaseAdopt(t.Context(), name, true)
	if err != nil || !adopted.Changed || adopted.Open || adopted.AllowConnections || adopted.Gen != 1 {
		t.Fatalf("adopt with termination: %+v, %v", adopted, err)
	}
	d = f.database(t, name)
	if d.AllowConnections || d.Marker.Kind != marker.Base || !d.Marker.ValidFor(name) || d.Marker.IsOpen() || d.Marker.Gen != 1 {
		t.Fatalf("adopt did not leave a closed generation-1 base: %+v", d)
	}
}

func TestAdoptFailureRestoresNullComment(t *testing.T) {
	f := newFixture(t, nil)
	name := f.source
	connectTestDB(t, f.svc.Config.DatabaseURL(name))
	if _, err := f.svc.BaseAdopt(t.Context(), name, false); config.ErrorCode(err) != config.DatabaseInUse {
		t.Fatalf("adopt with an active session = %v, want database_in_use", err)
	}
	d := f.database(t, name)
	if d.Comment != nil || !d.AllowConnections || d.Marker.PGCheckout != 0 {
		t.Fatalf("failed adopt did not restore NULL comment and allow connections: %+v", d)
	}
}

func TestCreateBaseCanceledLeavesNoBase(t *testing.T) {
	newFixture(t, func(t *testing.T, f *fixture) {
		name := f.svc.Config.Base
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		prefix := `COMMENT ON DATABASE "` + name + "__tmp_"
		var hits atomic.Int64
		svc, _ := f.viaProxy(t, func(sql string) testpg.QueryFault {
			if strings.HasPrefix(sql, prefix) && hits.Add(1) == 1 {
				cancel()
			}
			return testpg.ForwardQuery
		})
		_, err := svc.BaseCreate(ctx, name, f.source, false)
		if got := hits.Load(); got != 1 {
			t.Fatalf("temporary marker hook hits = %d, want 1 (base create error: %v)", got, err)
		}
		if config.ErrorCode(err) != config.Canceled {
			t.Fatalf("canceled create = %v, want canceled", err)
		}
		if f.exists(t, name) {
			t.Errorf("canceled create left base database %q", name)
		}
		if tmp := f.tmpDatabases(t); len(tmp) != 0 {
			t.Errorf("canceled create left temporary databases: %+v", tmp)
		}
	})
}

func TestCreateBaseCanceledBeforeRenameLeavesNoBase(t *testing.T) {
	newFixture(t, func(t *testing.T, f *fixture) {
		name := f.svc.Config.Base
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		prefix := `ALTER DATABASE "` + name + "__tmp_"
		const suffix = " WITH ALLOW_CONNECTIONS false"
		var hits atomic.Int64
		svc, _ := f.viaProxy(t, func(sql string) testpg.QueryFault {
			if strings.HasPrefix(sql, prefix) && strings.HasSuffix(sql, suffix) && hits.Add(1) == 1 {
				cancel()
			}
			return testpg.ForwardQuery
		})
		_, err := svc.BaseCreate(ctx, name, f.source, false)
		if got := hits.Load(); got != 1 {
			t.Fatalf("connection lockout hook hits = %d, want 1 (base create error: %v)", got, err)
		}
		if config.ErrorCode(err) != config.Canceled {
			t.Fatalf("canceled create = %v, want canceled", err)
		}
		if f.exists(t, name) {
			t.Errorf("canceled create left base database %q", name)
		}
		if tmp := f.tmpDatabases(t); len(tmp) != 0 {
			t.Errorf("canceled create left temporary databases: %+v", tmp)
		}
	})
}

func TestCreateBaseRecoveryLeftoverIsRemovableAfterBaseExists(t *testing.T) {
	var leftover string
	// newFixture creates the base through the real URL after this function returns.
	f := newFixture(t, func(t *testing.T, f *fixture) {
		base := f.svc.Config.Base
		// The subtest ends the fault proxy, so the backend holding the lost session's claim lock disconnects.
		passed := t.Run("lost CREATE reply", func(t *testing.T) {
			prefix := `CREATE DATABASE "` + base + "__tmp_"
			var first atomic.Bool
			svc, proxy := f.viaProxy(t, func(sql string) testpg.QueryFault {
				if strings.HasPrefix(sql, prefix) && !first.Swap(true) {
					return testpg.DropQueryReply
				}
				return testpg.ForwardQuery
			})
			_, err := svc.BaseCreate(t.Context(), base, f.source, false)
			if got := proxy.Injected(testpg.DropQueryReply); got != 1 {
				t.Fatalf("dropped CREATE replies = %d, want 1 (base create error: %v)", got, err)
			}
			tmp := f.tmpDatabases(t)
			if len(tmp) != 1 || !tmp[0].Marker.ValidFor(base) || tmp[0].Marker.Kind != marker.Tmp {
				t.Fatalf("want one managed temporary database, got %+v", tmp)
			}
			leftover = tmp[0].Name
			problem, ok := errors.AsType[*config.Error](err)
			if !ok || !strings.Contains(problem.Message, leftover) || !strings.Contains(problem.Message, "marked for cleanup") {
				t.Fatalf("base create error = %v, want *config.Error naming %q as marked for cleanup", err, leftover)
			}
		})
		if !passed {
			t.FailNow()
		}
		if f.exists(t, base) {
			t.Fatalf("failed create left base database %q", base)
		}
		if _, err := f.svc.GC(t.Context(), false); config.ErrorCode(err) != config.BaseNotFound {
			t.Fatalf("gc without base = %v, want base_not_found", err)
		}
		if _, err := f.svc.PoolDrain(t.Context()); config.ErrorCode(err) != config.BaseNotFound {
			t.Fatalf("pool drain without base = %v, want base_not_found", err)
		}
		if !f.exists(t, leftover) {
			t.Fatalf("leftover %q was removed without a base", leftover)
		}
	})
	drained, err := f.svc.PoolDrain(t.Context())
	if err != nil || !slices.Equal(drained.Dropped, []string{leftover}) {
		t.Fatalf("drain leftover after base create: %+v, %v", drained, err)
	}
	if tmp := f.tmpDatabases(t); len(tmp) != 0 {
		t.Fatalf("pool drain left temporary databases: %+v", tmp)
	}
}

func TestCopyMarkerBlackholedLeavesNoUnmarkedCopy(t *testing.T) {
	f := newFixture(t, nil)
	prefix := `COMMENT ON DATABASE "` + f.svc.Config.Base + "__tmp_"
	var first atomic.Bool
	svc, proxy := f.viaProxy(t, func(sql string) testpg.QueryFault {
		if strings.HasPrefix(sql, prefix) && !first.Swap(true) {
			return testpg.BlackholeConnection
		}
		return testpg.ForwardQuery
	})
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	_, err := svc.Ensure(ctx, "blackholed_marker", EnsureOptions{NoRefill: true})
	if ctx.Err() != nil {
		t.Fatalf("ensure did not return within 60 s: %v", err)
	}
	if got := proxy.Injected(testpg.BlackholeConnection); got != 1 {
		t.Fatalf("blackholed comments = %d, want 1 (ensure error: %v)", got, err)
	}
	// The retry marks the copy on a new connection, but the closed session cannot verify it, so the copy is discarded.
	if config.ErrorCode(err) != config.ConnectFailed || strings.Contains(err.Error(), "unmarked") || strings.Contains(err.Error(), "clean up temporary database") {
		t.Fatalf("ensure error = %v, want a failure after discarding the marked copy", err)
	}
	if tmp := f.tmpDatabases(t); len(tmp) != 0 {
		t.Fatalf("ensure left temporary databases: %+v", tmp)
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
	const warning = "1 per-database settings or grants could not be replicated from the base when this copy was made; run pgcheckout doctor"
	if claimed.Source != "pool" || !slices.Equal(claimed.Warnings, []string{warning}) {
		t.Fatalf("claim: source=%q, warnings=%v", claimed.Source, claimed.Warnings)
	}
}
