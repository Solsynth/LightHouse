package store

import (
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

// TestEntitiesMatchPresenceMigration pins the entity/GORM columns to the exact
// DDL of 0014_account_statuses_and_presence.sql. Those tables are shared with
// the C# fleet (create-time names came from the EF Core migrations), so a
// renamed field or a typo in a gorm tag would silently write to/skip a column —
// this test makes that a build-time failure instead of a production one.
func TestEntitiesMatchPresenceMigration(t *testing.T) {
	raw, err := os.ReadFile("../migrate/0014_account_statuses_and_presence.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	ddl := map[string]map[string]bool{}
	for _, table := range regexp.MustCompile(`(?s)CREATE TABLE IF NOT EXISTS (\w+) \((.*?)\n\);`).
		FindAllStringSubmatch(string(raw), -1) {
		name := table[1]
		columns := map[string]bool{}
		for _, line := range strings.Split(table[2], "\n") {
			line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ","))
			if line == "" || strings.HasPrefix(line, "CONSTRAINT") || strings.HasPrefix(line, "FOREIGN") {
				continue
			}
			columns[strings.Fields(line)[0]] = true
		}
		ddl[name] = columns
	}

	entities := map[string]any{
		"account_statuses":       &AccountStatusEntity{},
		"presence_activities":    &PresenceActivityEntity{},
		"presence_activity_tags": &PresenceActivityTagEntity{},
		"presence_catalog_items": &PresenceCatalogItemEntity{},
		"presence_catalog_tags":  &PresenceCatalogTagEntity{},
	}
	cache := &sync.Map{}
	namer := schema.NamingStrategy{}
	for table, dest := range entities {
		want := ddl[table]
		if len(want) == 0 {
			t.Fatalf("%s missing from the migration", table)
		}
		parsed, err := schema.Parse(dest, cache, namer)
		if err != nil {
			t.Fatalf("parse %s: %v", table, err)
		}
		got := map[string]bool{}
		for _, field := range parsed.Fields {
			if field.DBName != "" {
				got[field.DBName] = true
				if !want[field.DBName] {
					t.Errorf("%s: entity column %q has no matching SQL column", table, field.DBName)
				}
			}
		}
		for column := range want {
			if !got[column] {
				t.Errorf("%s: SQL column %q missing from the entity", table, column)
			}
		}
	}
}
