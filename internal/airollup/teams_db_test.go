package airollup

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/internal/testdbguard"
)

// G9 (02-DESIGN §17): AI money of one team never lands in another team's database, on real MySQL
// through the routing pool. Databases of this package only — the store tests own grbpwr_test.
const (
	g9One    = "airollup_one_test"
	g9Prefix = "airt_"
	g9Two    = g9Prefix + "b0000001"
	g9CP     = "airollup_cp_test"
)

func g9DSN(t *testing.T, db string) string {
	t.Helper()
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
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&multiStatements=true",
		os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD"), host, port, db)
}

func TestMain(m *testing.M) { os.Exit(g9Serialized(m)) }

// g9Serialized holds the server-wide lock the tenantdb and teamsync tests share: the disposable test
// MySQL keeps its data in memory, and two packages migrating full team schemas at once exhaust it.
func g9Serialized(m *testing.M) int {
	host := os.Getenv("MYSQL_HOST")
	if host == "" {
		return m.Run()
	}
	port := os.Getenv("MYSQL_PORT")
	if port == "" {
		port = "3306"
	}
	db, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s:%s)/", os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD"), host, port))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer conn.Close()
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK('teams_schema_tests', 900)`).Scan(&got); err != nil || got.Int64 != 1 {
		fmt.Fprintf(os.Stderr, "test schema lock: %v %v\n", got, err)
		return 1
	}
	defer conn.ExecContext(ctx, `SELECT RELEASE_LOCK('teams_schema_tests')`)
	return m.Run()
}

func count(t *testing.T, h *sql.DB, q string) int {
	t.Helper()
	var n int
	require.NoError(t, h.QueryRow(q).Scan(&n))
	return n
}

func TestG9AIMoneyStaysInTheSpendingTeam(t *testing.T) {
	d := g9DSN(t, g9One)
	require.NoError(t, testdbguard.CheckMySQLDSN(d))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	root, err := sql.Open("mysql", g9DSN(t, ""))
	require.NoError(t, err)
	defer root.Close()
	all := []string{g9One, g9Two, g9CP}
	for _, db := range all {
		for _, q := range []string{"DROP DATABASE IF EXISTS `" + db + "`", "CREATE DATABASE `" + db + "` CHARACTER SET utf8mb4"} {
			_, err := root.ExecContext(ctx, q)
			require.NoError(t, err)
		}
	}
	t.Cleanup(func() {
		for _, db := range all {
			_, _ = root.Exec("DROP DATABASE IF EXISTS `" + db + "`")
		}
	})
	// Team 2 gets the real migrations. Team #1 only the tables this test reads back (the container
	// keeps its data in memory; a second full schema is not worth it).
	_, err = store.MigrateTeam(ctx, d, g9Two)
	require.NoError(t, err)
	for _, tb := range []string{"ai_usage_event", "design_run", "design_run_attempt"} {
		_, err := root.ExecContext(ctx, "CREATE TABLE `"+g9One+"`.`"+tb+"` LIKE `"+g9Two+"`.`"+tb+"`")
		require.NoError(t, err, tb)
	}
	h1, err := sql.Open("mysql", d)
	require.NoError(t, err)
	defer h1.Close()
	d2, err := store.TeamDSN(d, g9Two)
	require.NoError(t, err)
	h2, err := sql.Open("mysql", d2)
	require.NoError(t, err)
	defer h2.Close()

	one := tenant.TeamOne(g9One)
	two := tenant.Team{ID: 2, Slug: "bravo", DBName: g9Two, Profile: tenant.ProfileStandard, Status: tenant.StatusActive}
	cfg := store.Config{DSN: d, MaxOpenConnections: 2, MaxIdleConnections: 2}
	cfg.Tenant.Team1 = one
	cfg.Tenant.DBPrefix = g9Prefix
	cfg.Tenant.Strict = true
	st, err := store.NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer st.Close()
	ctx2 := tenant.With(ctx, two)

	// 1. An AI call made with team 2's ctx: the ledger row lands in team 2's database only.
	utc := func() string { return "UTC" }
	led := aiprov.NewLedger(st.AI(), utc)
	h := led.Begin(ctx2, entity.AICallStart{ProviderKey: entity.AIProviderOpenRouter, Model: "m",
		Purpose: entity.AIPurposeNoteMarkdown, Actor: entity.AIActorSystem})
	require.NotNil(t, h)
	require.NotZero(t, h.ID, "the row was opened")
	led.Finish(ctx2, h, entity.AICallEnd{Status: entity.AICallOK, CostSource: entity.AICostProvider,
		CostUSD: decimal.NullDecimal{Decimal: decimal.RequireFromString("1.25"), Valid: true}})
	require.Equal(t, 1, count(t, h2, `SELECT COUNT(*) FROM ai_usage_event WHERE status = 'ok' AND cost_usd = 1.25`))
	require.Zero(t, count(t, h1, `SELECT COUNT(*) FROM ai_usage_event`), "nothing in team #1's database")

	// 2. A design run filed in team 2: a suspended team 2 is not claimed; an active one is claimed and
	// settled in its own database.
	res, err := h2.Exec(`INSERT INTO tech_card (style_number, name, brand, target_gender, top_category_id)
		VALUES ('G9-1', 'g9 probe', 'b', 'unisex', 1)`)
	require.NoError(t, err)
	card, err := res.LastInsertId()
	require.NoError(t, err)
	started, err := st.Design().StartRun(ctx2, entity.DesignRunStart{
		TechCardId: int(card), ClientRequestId: uuid.NewString(), Kind: entity.DesignRunKindFlat,
		RequestedOutputs: 1, Author: "probe",
		PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.10"), Valid: true},
	})
	require.NoError(t, err)
	claim := func(ctx context.Context) (claimed []entity.DesignRun) {
		require.NoError(t, WorkLoop(fakeDir{teams: []tenant.Team{one, twoAs(two, tenant.StatusSuspended)}}, nil, 1)(ctx,
			func(ctx context.Context) error {
				tm, _ := tenant.From(ctx)
				if tm.ID == 1 {
					return nil // team #1 holds no design tables beyond the copies; nothing to claim there
				}
				c, err := st.Design().ClaimRuns(ctx, 5, time.Minute, uuid.NewString())
				claimed = append(claimed, c...)
				return err
			}))
		return claimed
	}
	require.Empty(t, claim(ctx), "a suspended team's run is not claimed")
	var status string
	require.NoError(t, h2.QueryRow(`SELECT status FROM design_run WHERE id = ?`, started.Run.Id).Scan(&status))
	require.Equal(t, entity.DesignRunPending, status)

	var claimed []entity.DesignRun
	require.NoError(t, WorkLoop(fakeDir{teams: []tenant.Team{two}}, nil, 1)(ctx, func(ctx context.Context) error {
		c, err := st.Design().ClaimRuns(ctx, 5, time.Minute, uuid.NewString())
		claimed = append(claimed, c...)
		return err
	}))
	require.Len(t, claimed, 1)
	require.Equal(t, started.Run.Id, claimed[0].Id)
	_, err = st.Design().FailRun(ctx2, entity.DesignRunFail{RunId: claimed[0].Id, ClaimToken: claimed[0].ClaimToken.String,
		ErrorCode: "probe", LastError: "probe"})
	require.NoError(t, err)
	require.NoError(t, h2.QueryRow(`SELECT status FROM design_run WHERE id = ?`, started.Run.Id).Scan(&status))
	require.Equal(t, entity.DesignRunFailed, status, "settled in team 2's database")
	require.Zero(t, count(t, h1, `SELECT COUNT(*) FROM design_run`), "nothing in team #1's database")

	// 3. The rollup files team 2's spend under team 2's id in the control plane.
	cp, err := platformstore.Open(ctx, g9DSN(t, g9CP))
	require.NoError(t, err)
	defer cp.Close()
	w, err := New(Config{
		Dir:   fakeDir{teams: []tenant.Team{one, two}},
		Teams: st.DB(), Sink: cp, TZ: utc,
		Log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})
	require.NoError(t, err)
	w.RunOnce(ctx)
	today := time.Now().UTC().Truncate(24 * time.Hour)
	got, err := cp.AISpendSince(ctx, today)
	require.NoError(t, err)
	require.True(t, got[2].CostUSD.Equal(decimal.RequireFromString("1.25")), "team 2: %+v", got)
	require.Equal(t, int64(1), got[2].Calls)
	require.Zero(t, got[1].Calls, "team #1 spent nothing")
}

func twoAs(t tenant.Team, status tenant.Status) tenant.Team {
	t.Status = status
	return t
}
