// Copyright 2026 Brian Bouterse
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bridge

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migrate applies all pending database migrations. It creates a
// schema_migrations table to track which migrations have been applied.
// Migrations are embedded SQL files in the migrations/ directory,
// named with a numeric prefix (e.g., 001_initial_schema.sql).
// Each migration runs in a transaction. A PostgreSQL advisory lock
// prevents concurrent startup races.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	// Acquire advisory lock to prevent concurrent migration runs.
	// Use a fixed lock ID derived from "alcove-migrations".
	const lockID = 7_247_268_901 // arbitrary fixed number
	if _, err := pool.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		return fmt.Errorf("acquiring migration lock: %w", err)
	}
	defer pool.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockID)

	// Ensure the schema_migrations table exists.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`); err != nil {
		return fmt.Errorf("creating schema_migrations table: %w", err)
	}

	// Read which versions are already applied.
	applied := make(map[int]bool)
	rows, err := pool.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("reading applied migrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return fmt.Errorf("scanning migration version: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating applied migrations: %w", err)
	}

	// Read migration files from embedded FS.
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("reading migrations directory: %w", err)
	}

	// Sort by filename (numeric prefix ensures correct order).
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	// Apply pending migrations.
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		// Parse version from filename prefix (e.g., "001_initial_schema.sql" -> 1).
		version, err := parseVersion(entry.Name())
		if err != nil {
			return fmt.Errorf("parsing migration filename %q: %w", entry.Name(), err)
		}

		if applied[version] {
			continue
		}

		// Read the SQL.
		sql, err := fs.ReadFile(migrationFS, "migrations/"+entry.Name())
		if err != nil {
			return fmt.Errorf("reading migration %s: %w", entry.Name(), err)
		}

		// Execute in a transaction.
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("beginning transaction for migration %d: %w", version, err)
		}

		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("applying migration %d (%s): %w", version, entry.Name(), err)
		}

		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("recording migration %d: %w", version, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("committing migration %d: %w", version, err)
		}

		log.Printf("applied migration %03d: %s", version, entry.Name())
	}

	return nil
}

// parseVersion extracts the numeric prefix from a migration filename.
// "001_initial_schema.sql" -> 1, "002_add_schedules.sql" -> 2.
func parseVersion(filename string) (int, error) {
	parts := strings.SplitN(filename, "_", 2)
	if len(parts) == 0 {
		return 0, fmt.Errorf("no numeric prefix")
	}
	return strconv.Atoi(parts[0])
}
