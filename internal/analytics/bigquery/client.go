package bigquery

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/jekabolt/grbpwr-manager/internal/circuitbreaker"
	"google.golang.org/api/option"
)

// DefaultQueryTimeout is the default per-query timeout when not configured.
// BigQuery queries can take minutes; this prevents a single slow query from blocking the sync chain.
const DefaultQueryTimeout = 10 * time.Minute

// Config holds BigQuery client configuration.
type Config struct {
	ProjectID       string                `mapstructure:"project_id"`
	DatasetID       string                `mapstructure:"dataset_id"`
	CredentialsJSON string                `mapstructure:"credentials_json"`
	QueryTimeout    time.Duration         `mapstructure:"query_timeout"` // per-query timeout; 0 = use DefaultQueryTimeout
	UseLiteralDates bool                  `mapstructure:"use_literal_dates"` // if true, embed dates in SQL instead of params (workaround for param serialization issues)
	CircuitBreaker  circuitbreaker.Config `mapstructure:"circuit_breaker"`
}

// Client wraps the BigQuery client for analytics queries.
type Client struct {
	client          *bigquery.Client
	projectID       string
	datasetID       string
	queryTimeout    time.Duration
	useLiteralDates bool
	circuitBreaker  *circuitbreaker.CircuitBreaker
	now             func() time.Time // for testing; nil = time.Now
}

// NewClient creates a new BigQuery analytics client.
// Returns (nil, nil) when cfg is nil or project_id/dataset_id are not set.
func NewClient(ctx context.Context, cfg *Config) (*Client, error) {
	if cfg == nil || cfg.ProjectID == "" || cfg.DatasetID == "" {
		slog.Default().InfoContext(ctx, "BigQuery analytics disabled (not configured)")
		return nil, nil
	}

	var opts []option.ClientOption
	if cfg.CredentialsJSON != "" {
		jsonBytes := []byte(cfg.CredentialsJSON)
		if len(jsonBytes) > 0 && jsonBytes[0] == '{' {
			opts = append(opts, option.WithAuthCredentialsJSON(option.ServiceAccount, jsonBytes))
		} else {
			if _, err := os.Stat(cfg.CredentialsJSON); err != nil {
				if os.IsNotExist(err) {
					return nil, fmt.Errorf("credentials file not found: %s", cfg.CredentialsJSON)
				}
				return nil, fmt.Errorf("credentials file inaccessible: %w", err)
			}
			opts = append(opts, option.WithAuthCredentialsFile(option.ServiceAccount, cfg.CredentialsJSON))
		}
	}

	client, err := bigquery.NewClient(ctx, cfg.ProjectID, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create BigQuery client: %w", err)
	}

	queryTimeout := cfg.QueryTimeout
	if queryTimeout == 0 {
		queryTimeout = DefaultQueryTimeout
	}

	cb := circuitbreaker.New("bigquery", cfg.CircuitBreaker, func(from, to circuitbreaker.State, reason string) {
		slog.Default().ErrorContext(context.Background(), "BigQuery circuit breaker state changed",
			slog.String("from", from.String()),
			slog.String("to", to.String()),
			slog.String("reason", reason))
	})

	slog.Default().InfoContext(ctx, "BigQuery analytics client initialized",
		slog.String("project_id", cfg.ProjectID),
		slog.String("dataset_id", cfg.DatasetID),
		slog.Duration("query_timeout", queryTimeout),
		slog.Bool("use_literal_dates", cfg.UseLiteralDates))

	return &Client{
		client:          client,
		projectID:       cfg.ProjectID,
		datasetID:       cfg.DatasetID,
		queryTimeout:    queryTimeout,
		useLiteralDates: cfg.UseLiteralDates,
		circuitBreaker:  cb,
	}, nil
}

// queryContext returns ctx with per-query timeout. Caller must call cancel when done.
// When queryTimeout is 0 (e.g. manually constructed), no timeout is applied.
// NewClient normalizes 0 to DefaultQueryTimeout, so this path is rare in production.
func (c *Client) queryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.queryTimeout > 0 {
		return context.WithTimeout(ctx, c.queryTimeout)
	}
	return ctx, func() {}
}

// tableRef returns the wildcard table reference for GA4 event tables.
// events_* matches both events_YYYYMMDD (daily) and events_intraday_YYYYMMDD
// tables, so a single wildcard is sufficient for all queries.
func (c *Client) tableRef() string {
	return fmt.Sprintf("`%s.%s.events_*`", c.projectID, c.datasetID)
}

// needsIntraday returns true when the query date range includes today,
// meaning the not-yet-finalized intraday table must be included.
func (c *Client) needsIntraday(endDate time.Time) bool {
	now := time.Now
	if c != nil && c.now != nil {
		now = c.now
	}
	today := now().UTC().Truncate(24 * time.Hour)
	return !endDate.Before(today)
}

// eventsSourceColumns returns a SQL subquery that scans only the requested
// columns from the GA4 events_* wildcard table, filtered by _TABLE_SUFFIX.
//
// Key insight: events_* already matches events_intraday_YYYYMMDD tables
// (suffix = "intraday_YYYYMMDD"), so NO UNION ALL is needed. We use a single
// wildcard with an extended suffix filter when intraday data is required.
//
// When endDate < today the intraday branch of the OR is omitted entirely,
// so BigQuery only resolves daily table metadata — zero wasted scans.
//
// Column names are validated to prevent SQL injection.
// Allowed patterns:
//  - Simple identifiers: event_name, user_pseudo_id
//  - Nested field access: device.category, geo.country
//  - With AS clause: device.category AS device_category
//
// Security: This regex is intentionally strict to prevent injection attacks.
// It only allows alphanumeric characters, underscores, and dots for field access.
// Spaces are ONLY allowed in the context of "AS alias" syntax.
var (
	// allowedColumnRegex validates the complete column expression
	allowedColumnRegex = regexp.MustCompile(`^[a-zA-Z0-9_.]+( AS [a-zA-Z0-9_]+)?$`)
	
	// fieldPartRegex validates each part of a field path (before and after AS)
	fieldPartRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*(\.[a-zA-Z][a-zA-Z0-9_]*)*$`)
)

// validateColumnName performs strict validation on column names to prevent SQL injection.
// It checks both the overall pattern and validates individual field components.
func validateColumnName(col string) error {
	// First check: overall pattern must match
	if !allowedColumnRegex.MatchString(col) {
		return fmt.Errorf("invalid column name pattern: %q", col)
	}
	
	// Extract base field and alias (if present)
	parts := strings.Split(col, " AS ")
	baseField := parts[0]
	var alias string
	if len(parts) == 2 {
		alias = parts[1]
	}
	
	// Validate base field structure
	if !fieldPartRegex.MatchString(baseField) {
		return fmt.Errorf("invalid base field name: %q", baseField)
	}
	
	// Validate alias if present
	if alias != "" && !fieldPartRegex.MatchString(alias) {
		return fmt.Errorf("invalid alias name: %q", alias)
	}
	
	// Additional security checks
	colLower := strings.ToLower(col)
	
	// Prevent SQL keywords that could be injection vectors
	dangerousKeywords := []string{
		"select", "insert", "update", "delete", "drop", "create",
		"alter", "truncate", "exec", "execute", "union", "into",
		"from", "where", "join", "on", "values", "set",
		"declare", "cast", "convert", "char", "varchar",
		";", "--", "/*", "*/", "xp_", "sp_",
	}
	
	for _, keyword := range dangerousKeywords {
		if strings.Contains(colLower, keyword) {
			return fmt.Errorf("column name contains forbidden keyword: %q", col)
		}
	}
	
	// Prevent potential comment or statement terminators
	if strings.Contains(col, ";") || strings.Contains(col, "--") || 
	   strings.Contains(col, "/*") || strings.Contains(col, "*/") {
		return fmt.Errorf("column name contains forbidden characters: %q", col)
	}
	
	return nil
}

func (c *Client) eventsSourceColumns(startDate, endDate time.Time, columns ...string) (string, error) {
	needsIntraday := c.needsIntraday(endDate)
	startStr := startDate.UTC().Format("2006-01-02")
	endStr := endDate.UTC().Format("2006-01-02")
	startSuffix := startDate.UTC().Format("20060102")
	endSuffix := endDate.UTC().Format("20060102")

	slog.Default().InfoContext(context.Background(), "bq eventsSourceColumns",
		slog.String("start_date", startStr),
		slog.String("end_date", endStr),
		slog.String("start_date_tz", startDate.Format(time.RFC3339)),
		slog.String("end_date_tz", endDate.Format(time.RFC3339)),
		slog.String("start_suffix", startSuffix),
		slog.String("end_suffix", endSuffix),
		slog.Bool("needs_intraday", needsIntraday),
		slog.Bool("use_literal_dates", c.useLiteralDates),
		slog.String("table_ref", c.tableRef()))

	colList := "*"
	if len(columns) > 0 {
		colList = ""
		for i, col := range columns {
			if err := validateColumnName(col); err != nil {
				return "", err
			}
			if i > 0 {
				colList += ", "
			}
			colList += col
		}
	}

	var dailyFilter, intradayFilter string
	if c.useLiteralDates {
		dailyFilter = fmt.Sprintf("_TABLE_SUFFIX BETWEEN '%s' AND '%s'", startSuffix, endSuffix)
		intradayFilter = fmt.Sprintf("_TABLE_SUFFIX BETWEEN 'intraday_%s' AND 'intraday_%s'", startSuffix, endSuffix)
	} else {
		dailyFilter = `_TABLE_SUFFIX BETWEEN FORMAT_DATE('%%Y%%m%%d', DATE(@start_date)) AND FORMAT_DATE('%%Y%%m%%d', DATE(@end_date))`
		intradayFilter = `_TABLE_SUFFIX BETWEEN CONCAT('intraday_', FORMAT_DATE('%%Y%%m%%d', DATE(@start_date))) AND CONCAT('intraday_', FORMAT_DATE('%%Y%%m%%d', DATE(@end_date)))`
	}

	if !needsIntraday {
		return fmt.Sprintf("(SELECT %s FROM %s WHERE %s)", colList, c.tableRef(), dailyFilter), nil
	}
	return fmt.Sprintf("(SELECT %s FROM %s WHERE %s OR %s)", colList, c.tableRef(), dailyFilter, intradayFilter), nil
}

// withCircuitBreaker wraps a query function with circuit breaker protection.
func (c *Client) withCircuitBreaker(ctx context.Context, fn func(context.Context) error) error {
	if c.circuitBreaker == nil {
		return fn(ctx)
	}
	return c.circuitBreaker.Call(ctx, fn)
}

// CircuitBreakerState returns the current state of the circuit breaker.
func (c *Client) CircuitBreakerState() circuitbreaker.State {
	if c.circuitBreaker == nil {
		return circuitbreaker.StateClosed
	}
	return c.circuitBreaker.State()
}

// Close closes the underlying BigQuery client.
func (c *Client) Close() error {
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}
