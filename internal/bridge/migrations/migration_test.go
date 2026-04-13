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

package migrations

import (
	"os"
	"strings"
	"testing"
)

// TestPersonalAPITokensMigrationStructure tests the structure of the migration file
func TestPersonalAPITokensMigrationStructure(t *testing.T) {
	migrationContent, err := os.ReadFile("025_personal_api_tokens.sql")
	if err != nil {
		t.Fatalf("failed to read migration file: %v", err)
	}

	content := string(migrationContent)

	// Test that the migration contains the expected table creation
	expectedElements := []string{
		"CREATE TABLE IF NOT EXISTS personal_api_tokens",
		"id TEXT PRIMARY KEY",
		"username TEXT NOT NULL",
		"name TEXT NOT NULL DEFAULT ''",
		"token_hash TEXT NOT NULL",
		"created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()",
		"last_accessed_at TIMESTAMPTZ",
		"FOREIGN KEY (username) REFERENCES auth_users(username) ON DELETE CASCADE",
		"CREATE INDEX IF NOT EXISTS idx_personal_api_tokens_username",
	}

	for _, element := range expectedElements {
		if !strings.Contains(content, element) {
			t.Errorf("migration file missing expected element: %s", element)
		}
	}

	// Test that the migration doesn't contain potentially problematic elements
	problematicElements := []string{
		"DROP TABLE",
		"DELETE FROM",
		"TRUNCATE",
	}

	for _, element := range problematicElements {
		if strings.Contains(strings.ToUpper(content), strings.ToUpper(element)) {
			t.Errorf("migration file contains potentially problematic element: %s", element)
		}
	}
}

// TestMigrationFileNaming tests that the migration file follows the correct naming convention
func TestMigrationFileNaming(t *testing.T) {
	// Check that the file exists with the correct name
	if _, err := os.Stat("025_personal_api_tokens.sql"); os.IsNotExist(err) {
		t.Error("expected migration file 025_personal_api_tokens.sql to exist")
	}

	// Check that the naming follows the pattern: number_description.sql
	fileName := "025_personal_api_tokens.sql"
	if !strings.HasSuffix(fileName, ".sql") {
		t.Error("migration file should have .sql extension")
	}

	parts := strings.Split(strings.TrimSuffix(fileName, ".sql"), "_")
	if len(parts) < 2 {
		t.Error("migration file should follow pattern: number_description.sql")
	}

	// First part should be a number
	if parts[0] != "025" {
		t.Errorf("expected migration number 025, got %s", parts[0])
	}
}

// TestMigrationSQLSyntax tests that the migration contains valid SQL structure
func TestMigrationSQLSyntax(t *testing.T) {
	migrationContent, err := os.ReadFile("025_personal_api_tokens.sql")
	if err != nil {
		t.Fatalf("failed to read migration file: %v", err)
	}

	content := strings.TrimSpace(string(migrationContent))

	// Should not be empty
	if len(content) == 0 {
		t.Error("migration file should not be empty")
	}

	// Should end with semicolon
	if !strings.HasSuffix(content, ";") {
		t.Error("migration file should end with semicolon")
	}

	// Should not contain multiple consecutive empty lines (indicates good formatting)
	if strings.Contains(content, "\n\n\n") {
		t.Error("migration file should not contain excessive empty lines")
	}

	// Should contain CREATE statements
	if !strings.Contains(strings.ToUpper(content), "CREATE") {
		t.Error("migration file should contain CREATE statements")
	}
}

// TestMigrationTableConstraints tests the table constraints in the migration
func TestMigrationTableConstraints(t *testing.T) {
	migrationContent, err := os.ReadFile("025_personal_api_tokens.sql")
	if err != nil {
		t.Fatalf("failed to read migration file: %v", err)
	}

	content := strings.ToUpper(string(migrationContent))

	// Test for PRIMARY KEY
	if !strings.Contains(content, "PRIMARY KEY") {
		t.Error("table should have a primary key")
	}

	// Test for NOT NULL constraints
	if !strings.Contains(content, "NOT NULL") {
		t.Error("table should have NOT NULL constraints")
	}

	// Test for FOREIGN KEY constraint
	if !strings.Contains(content, "FOREIGN KEY") {
		t.Error("table should have foreign key constraint to auth_users")
	}

	// Test for CASCADE delete
	if !strings.Contains(content, "ON DELETE CASCADE") {
		t.Error("foreign key should have ON DELETE CASCADE")
	}

	// Test for index creation
	if !strings.Contains(content, "CREATE INDEX") {
		t.Error("migration should create index on username")
	}
}

// Note: The following tests would require a real PostgreSQL database and are marked as integration tests:
//
// - TestMigrationExecution: Run the migration against a test database to ensure it executes successfully
// - TestMigrationRollback: Test that the migration can be rolled back if needed
// - TestForeignKeyConstraint: Test that the foreign key constraint works correctly
// - TestIndexCreation: Test that the index is created and improves query performance
// - TestTablePermissions: Test that the table has appropriate permissions
// - TestMigrationIdempotency: Test that running the migration multiple times is safe (IF NOT EXISTS clauses)
//
// These integration tests should be run with a test PostgreSQL instance and would verify:
// 1. Migration executes without errors
// 2. Table is created with correct schema
// 3. Constraints are properly enforced
// 4. Index improves query performance for username lookups
// 5. Foreign key cascade delete works correctly
// 6. Migration is idempotent (can be run multiple times safely)