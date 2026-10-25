// Package safety checks new SQL migrations for schema changes that MySQL
// validates against historical rows.
package safety

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/rubenv/sql-migrate/sqlparse"
)

// Risk identifies a schema change that MySQL validates against every existing
// row in a table. A new migration may intentionally make one of these changes,
// but it must record how the existing data was checked first.
type Risk string

const (
	RiskCheckConstraint Risk = "CHECK constraint"
	RiskNotNull         Risk = "NOT NULL change"
	RiskUnique          Risk = "UNIQUE index"
	RiskModifyColumn    Risk = "MODIFY/CHANGE column"
)

// Finding is one unacknowledged, history-validating schema change in a new
// migration file.
type Finding struct {
	File      string
	Table     string
	Risk      Risk
	Statement string
}

// Report describes which head-only migration files were inspected and the
// unsafe changes that lacked a data-checked annotation.
type Report struct {
	NewFiles []string
	Findings []Finding
}

const identifierPattern = "(?:`[^`]+`|[a-zA-Z0-9_$]+)"

var (
	createTableSafetyRe = regexp.MustCompile(
		`(?i)\bcreate\s+(?:temporary\s+)?table\s+(?:if\s+not\s+exists\s+)?` +
			`(?:(?:` + identifierPattern + `)\s*\.\s*)?(` + identifierPattern + `)`,
	)
	alterTableSafetyRe = regexp.MustCompile(
		`(?i)\balter\s+table\s+(?:if\s+exists\s+)?` +
			`(?:(?:` + identifierPattern + `)\s*\.\s*)?(` + identifierPattern + `)`,
	)
	createUniqueSafetyRe = regexp.MustCompile(
		`(?i)\bcreate\s+unique\s+(?:index|key)\s+(?:if\s+not\s+exists\s+)?` + identifierPattern +
			`(?:\s+using\s+` + identifierPattern + `)?\s+on\s+` +
			`(?:(?:` + identifierPattern + `)\s*\.\s*)?(` + identifierPattern + `)`,
	)
	addCheckSafetyRe = regexp.MustCompile(
		`(?is)\badd\b.*?\bcheck\s*\(`,
	)
	addUniqueSafetyRe = regexp.MustCompile(
		`(?is)\badd\b.*?\bunique\b`,
	)
	modifyColumnSafetyRe = regexp.MustCompile(
		`(?i)\b(?:modify|change)\s+(?:column\s+)?` + identifierPattern,
	)
	setNotNullSafetyRe = regexp.MustCompile(
		`(?i)\balter\s+(?:column\s+)?` + identifierPattern + `\s+set\s+not\s+null\b`,
	)
	dataCheckedSafetyRe = regexp.MustCompile(`(?im)^[ \t]*--[ \t]*data-checked:[ \t]*\S.*$`)
)

type migrationFile struct {
	name       string
	body       []byte
	statements []string
}

// LintDirs compares two migration directories. Only SQL files present in head
// and absent from base are linted. Tables created anywhere in base are treated
// as pre-existing because all base migrations have already run when a PR is
// deployed. Tables created by an earlier new migration are also pre-existing
// for later new files.
func LintDirs(baseDir, headDir string) (Report, error) {
	base, err := readMigrationDir(baseDir)
	if err != nil {
		return Report{}, fmt.Errorf("read base migrations: %w", err)
	}
	head, err := readMigrationDir(headDir)
	if err != nil {
		return Report{}, fmt.Errorf("read head migrations: %w", err)
	}

	baseNames := make(map[string]struct{}, len(base))
	existingTables := make(map[string]struct{})
	for _, migration := range base {
		baseNames[migration.name] = struct{}{}
		addCreatedTables(existingTables, migration.statements)
	}

	var report Report
	for _, migration := range head {
		if _, existedAtBase := baseNames[migration.name]; existedAtBase {
			continue
		}

		report.NewFiles = append(report.NewFiles, migration.name)
		if !dataCheckedSafetyRe.Match(migration.body) {
			report.Findings = append(report.Findings, findRisks(migration, existingTables)...)
		}

		// Deliberately update history only after linting this file: a table
		// created and constrained in one migration has no historical rows.
		addCreatedTables(existingTables, migration.statements)
	}

	return report, nil
}

func readMigrationDir(dir string) ([]migrationFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	files := make([]migrationFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".sql") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		parsed, err := sqlparse.ParseMigration(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", entry.Name(), err)
		}
		files = append(files, migrationFile{
			name:       entry.Name(),
			body:       body,
			statements: parsed.UpStatements,
		})
	}

	// sql-migrate applies files by their full filename/ID, not by parsed numeric
	// prefix. Match that ordering so a pair of new migrations is evaluated in
	// the same history order as deployment.
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return files, nil
}

func addCreatedTables(existing map[string]struct{}, statements []string) {
	for _, statement := range statements {
		for _, match := range createTableSafetyRe.FindAllStringSubmatch(statement, -1) {
			existing[normalizeIdentifier(match[1])] = struct{}{}
		}
	}
}

func findRisks(migration migrationFile, existingTables map[string]struct{}) []Finding {
	var findings []Finding
	for _, statement := range migration.statements {
		alterMatches := alterTableSafetyRe.FindAllStringSubmatchIndex(statement, -1)
		for i, match := range alterMatches {
			table := normalizeIdentifier(statement[match[2]:match[3]])
			if _, exists := existingTables[table]; !exists {
				continue
			}

			end := len(statement)
			if i+1 < len(alterMatches) {
				end = alterMatches[i+1][0]
			}
			clause := statement[match[1]:end]
			seen := make(map[Risk]struct{})
			appendRisk := func(risk Risk) {
				if _, duplicate := seen[risk]; duplicate {
					return
				}
				seen[risk] = struct{}{}
				findings = append(findings, Finding{
					File:      migration.name,
					Table:     table,
					Risk:      risk,
					Statement: compactStatement(statement),
				})
			}

			if addCheckSafetyRe.MatchString(clause) {
				appendRisk(RiskCheckConstraint)
			}
			if addUniqueSafetyRe.MatchString(clause) {
				appendRisk(RiskUnique)
			}
			if modifyColumnSafetyRe.MatchString(clause) {
				appendRisk(RiskModifyColumn)
			}
			if setNotNullSafetyRe.MatchString(clause) {
				appendRisk(RiskNotNull)
			}
		}

		for _, match := range createUniqueSafetyRe.FindAllStringSubmatch(statement, -1) {
			table := normalizeIdentifier(match[1])
			if _, exists := existingTables[table]; !exists {
				continue
			}
			findings = append(findings, Finding{
				File:      migration.name,
				Table:     table,
				Risk:      RiskUnique,
				Statement: compactStatement(statement),
			})
		}
	}
	return findings
}

func normalizeIdentifier(identifier string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(identifier), "`"))
}

func compactStatement(statement string) string {
	const maxLength = 240
	compact := strings.Join(strings.Fields(statement), " ")
	runes := []rune(compact)
	if len(runes) <= maxLength {
		return compact
	}
	return string(runes[:maxLength-3]) + "..."
}
