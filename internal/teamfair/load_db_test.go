package teamfair

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/store/tenantdb"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/internal/testdbguard"
	"github.com/jmoiron/sqlx"
	"google.golang.org/grpc"
)

// TestLoadHeavyTeamDoesNotSlowOthers is the X15 load test on a real MySQL (run through
// ~/.claude/scripts/test-db.sh): one routing pool of 5 connections (lekalo's size) shared by two
// team databases. Team A floods heavy calls (a 400 ms transaction each) and slow light calls
// (150 ms) from 24 clients; team B makes one quick call every 25 ms. The same flood runs with the
// gate off and with the default gate; B's latency is reported for both and bounded with the gate.
func TestLoadHeavyTeamDoesNotSlowOthers(t *testing.T) {
	host := os.Getenv("MYSQL_HOST")
	if host == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("CI set but MYSQL_HOST empty")
		}
		t.Skip("no test MySQL (run through test-db.sh)")
	}
	port := os.Getenv("MYSQL_PORT")
	if port == "" {
		port = "3306"
	}
	home := os.Getenv("MYSQL_DATABASE")
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true", os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD"), host, port, home)
	if err := testdbguard.CheckMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	const prefix = "tfair_"
	dbB := prefix + "b0000001"
	raw, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec("CREATE DATABASE IF NOT EXISTS `" + dbB + "`"); err != nil {
		t.Fatal(err)
	}
	defer raw.Exec("DROP DATABASE IF EXISTS `" + dbB + "`")

	one := tenant.TeamOne(home)
	two := tenant.Team{ID: 2, Slug: "bravo", DBName: dbB, Status: tenant.StatusActive}
	db, _, err := tenantdb.Open(dsn, tenantdb.Options{Team1: one, DBPrefix: prefix})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const pool = 5
	db.SetMaxOpenConns(pool)
	db.SetMaxIdleConns(pool)

	off := run(t, db, New(Config{Off: true}), one, two)
	on := run(t, db, New(Defaults(pool)), one, two)
	t.Logf("pool %d; team A: 12 heavy + 12 slow clients; team B: one quick call every 25 ms", pool)
	t.Logf("gate off: B p50 %v p95 %v max %v; A done heavy %d light %d",
		off.p50, off.p95, off.max, off.heavy, off.light)
	t.Logf("gate on:  B p50 %v p95 %v max %v; A done heavy %d light %d, A refused %d",
		on.p50, on.p95, on.max, on.heavy, on.light, on.refused)
	if on.p95 > 100*time.Millisecond {
		t.Fatalf("with the gate team B's p95 is %v, want ≤ 100ms", on.p95)
	}
	if on.heavy == 0 || on.light == 0 {
		t.Fatal("team A made no progress with the gate")
	}
}

type loadResult struct {
	p50, p95, max         time.Duration
	heavy, light, refused int64
}

func run(t *testing.T, db *sqlx.DB, g *Gate, a, b tenant.Team) loadResult {
	t.Helper()
	e := NewEdge(g, nil, nil)
	call := func(ctx context.Context, method string, h func(ctx context.Context) error) error {
		_, err := e.Unary()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: method}, func(ctx context.Context, _ any) (any, error) {
			return nil, h(ctx)
		})
		return err
	}
	heavyJob := func(ctx context.Context) error { // a long transaction: holds one connection
		tx, err := db.BeginTxx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		_, err = tx.ExecContext(ctx, "SELECT SLEEP(0.4)")
		return err
	}
	slowJob := func(ctx context.Context) error {
		_, err := db.ExecContext(ctx, "SELECT SLEEP(0.15)")
		return err
	}
	quickJob := func(ctx context.Context) error {
		var n int
		return db.GetContext(ctx, &n, "SELECT 1")
	}

	ctxA := tenant.With(context.Background(), a)
	ctxB := tenant.With(context.Background(), b)
	stop := make(chan struct{})
	var heavy, light, refused atomic.Int64
	var wg sync.WaitGroup
	flood := func(method string, job func(context.Context) error, done *atomic.Int64) {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := call(ctxA, method, job); err != nil {
				refused.Add(1)
				time.Sleep(20 * time.Millisecond) // a client backs off and retries
				continue
			}
			done.Add(1)
		}
	}
	for i := 0; i < 12; i++ {
		wg.Add(2)
		go flood("/admin.AdminService/ExportTechCardArchive", heavyJob, &heavy)
		go flood("/admin.AdminService/ListTasks", slowJob, &light)
	}
	time.Sleep(500 * time.Millisecond) // let the flood fill the pool

	lat := make([]time.Duration, 0, 80)
	for i := 0; i < 80; i++ {
		start := time.Now()
		if err := call(ctxB, "/admin.AdminService/GetTechCard", quickJob); err != nil {
			t.Fatalf("team B refused: %v", err)
		}
		lat = append(lat, time.Since(start))
		time.Sleep(25 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	r := loadResult{
		p50: lat[len(lat)/2].Round(time.Millisecond), p95: lat[len(lat)*95/100].Round(time.Millisecond),
		max: lat[len(lat)-1].Round(time.Millisecond), heavy: heavy.Load(), light: light.Load(), refused: refused.Load(),
	}
	return r
}
