package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var migrationTableNameRE = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

type MigrationConfig struct {
	Enabled   bool   `mapstructure:"enabled" yaml:"enabled"`
	Dir       string `mapstructure:"dir" yaml:"dir"`
	TableName string `mapstructure:"table_name" yaml:"table_name"`
}

func (c MigrationConfig) withDefaults() MigrationConfig {
	if c.TableName == "" {
		c.TableName = "schema_migrations"
	}
	return c
}

type Migrator struct {
	db  *sql.DB
	cfg MigrationConfig
}

func NewMigrator(db *sql.DB, cfg MigrationConfig) (*Migrator, error) {
	if db == nil {
		return nil, fmt.Errorf("migrator db is nil")
	}
	cfg = cfg.withDefaults()
	if cfg.Dir == "" {
		return nil, fmt.Errorf("migration dir is empty")
	}
	if !migrationTableNameRE.MatchString(cfg.TableName) {
		return nil, fmt.Errorf("invalid migration table name: %s", cfg.TableName)
	}
	return &Migrator{db: db, cfg: cfg}, nil
}

func (m *Migrator) Run(ctx context.Context) error {
	files, err := m.discoverFiles()
	if err != nil {
		return err
	}
	if err := m.ensureMetadataTable(ctx); err != nil {
		return err
	}
	applied, err := m.loadAppliedVersions(ctx)
	if err != nil {
		return err
	}
	for _, file := range files {
		version := filepath.Base(file)
		if applied[version] {
			continue
		}
		if err := m.applyFile(ctx, version, file); err != nil {
			return err
		}
	}
	return nil
}

func (m *Migrator) discoverFiles() ([]string, error) {
	pattern := filepath.Join(m.cfg.Dir, "*.sql")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("discover migration files: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

func (m *Migrator) ensureMetadataTable(ctx context.Context) error {
	stmt := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
  version VARCHAR(255) NOT NULL,
  checksum CHAR(64) NOT NULL,
  applied_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
`, m.cfg.TableName)
	if _, err := m.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("ensure migration metadata table: %w", err)
	}
	return nil
}

func (m *Migrator) loadAppliedVersions(ctx context.Context) (map[string]bool, error) {
	stmt := fmt.Sprintf("SELECT version FROM %s", m.cfg.TableName)
	rows, err := m.db.QueryContext(ctx, stmt)
	if err != nil {
		return nil, fmt.Errorf("query applied migrations: %w", err)
	}
	defer rows.Close()

	result := make(map[string]bool)
	for rows.Next() {
		var version string
		if scanErr := rows.Scan(&version); scanErr != nil {
			return nil, fmt.Errorf("scan applied migration: %w", scanErr)
		}
		result[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied migrations: %w", err)
	}
	return result, nil
}

func (m *Migrator) applyFile(ctx context.Context, version, path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read migration file %s: %w", path, err)
	}
	statements := splitSQLStatements(string(content))
	if len(statements) == 0 {
		return fmt.Errorf("migration %s has no executable statement", version)
	}

	checksum := sha256.Sum256(content)
	checksumHex := hex.EncodeToString(checksum[:])

	for idx, stmt := range statements {
		if _, execErr := m.db.ExecContext(ctx, stmt); execErr != nil {
			return fmt.Errorf("execute migration %s statement %d: %w", version, idx+1, execErr)
		}
	}
	insertStmt := fmt.Sprintf("INSERT INTO %s (version, checksum, applied_at) VALUES (?, ?, ?)", m.cfg.TableName)
	if _, err := m.db.ExecContext(ctx, insertStmt, version, checksumHex, time.Now().UTC()); err != nil {
		return fmt.Errorf("record migration %s: %w", version, err)
	}
	return nil
}

func splitSQLStatements(sqlContent string) []string {
	var (
		statements      []string
		builder         strings.Builder
		inSingleQuote   bool
		inDoubleQuote   bool
		inBacktick      bool
		inLineComment   bool
		inBlockComment  bool
		previousEscaped bool
	)
	runes := []rune(sqlContent)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		var next rune
		if i+1 < len(runes) {
			next = runes[i+1]
		}

		if inLineComment {
			if ch == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			if ch == '*' && next == '/' {
				inBlockComment = false
				i++
			}
			continue
		}
		if !inSingleQuote && !inDoubleQuote && !inBacktick {
			if ch == '-' && next == '-' {
				inLineComment = true
				i++
				continue
			}
			if ch == '#' {
				inLineComment = true
				continue
			}
			if ch == '/' && next == '*' {
				inBlockComment = true
				i++
				continue
			}
		}

		if !inDoubleQuote && !inBacktick && ch == '\'' && !previousEscaped {
			inSingleQuote = !inSingleQuote
		} else if !inSingleQuote && !inBacktick && ch == '"' && !previousEscaped {
			inDoubleQuote = !inDoubleQuote
		} else if !inSingleQuote && !inDoubleQuote && ch == '`' && !previousEscaped {
			inBacktick = !inBacktick
		}

		if ch == ';' && !inSingleQuote && !inDoubleQuote && !inBacktick {
			stmt := strings.TrimSpace(builder.String())
			if stmt != "" {
				statements = append(statements, stmt)
			}
			builder.Reset()
			previousEscaped = false
			continue
		}

		builder.WriteRune(ch)
		previousEscaped = ch == '\\' && !previousEscaped
		if ch != '\\' {
			previousEscaped = false
		}
	}
	last := strings.TrimSpace(builder.String())
	if last != "" {
		statements = append(statements, last)
	}
	return statements
}

type MigrationComponent struct {
	migrator *Migrator
}

func NewMigrationComponent(db *sql.DB, cfg MigrationConfig) (*MigrationComponent, error) {
	if !cfg.Enabled {
		return &MigrationComponent{}, nil
	}
	migrator, err := NewMigrator(db, cfg)
	if err != nil {
		return nil, err
	}
	return &MigrationComponent{migrator: migrator}, nil
}

func (c *MigrationComponent) Start(ctx context.Context) error {
	if c == nil || c.migrator == nil {
		return nil
	}
	return c.migrator.Run(ctx)
}

func (c *MigrationComponent) Stop(_ context.Context) error {
	return nil
}
