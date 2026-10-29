// stargate-module-import copies legacy Padlock/Passport rows into a module's
// new table layout. It is intentionally separate from the full-fleet
// stargate-migrate tool: use this when a module's table structure changed and
// the module owns an importer.
//
// Usage:
//
//	stargate-module-import \
//	  --module app.solian.id.relationships \
//	  --source-dsn "postgres://.../dyson_passport" \
//	  --target-dsn "postgres://.../dyson_stargate"
//
// Run Stargate once before importing so the core schema exists. The importer
// creates the module table itself when it can.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"gorm.io/gorm"

	"src.solsynth.dev/sosys/stargate/internal/config"
	"src.solsynth.dev/sosys/stargate/internal/db"
	"src.solsynth.dev/sosys/stargate/internal/module"
	"src.solsynth.dev/sosys/stargate/internal/modules/standard"
)

func main() {
	var moduleID, sourceDSN, targetDSN string
	var dryRun bool
	var batchSize int
	flag.StringVar(&moduleID, "module", "", "module ID (or \"all\" for every module with an importer)")
	flag.StringVar(&sourceDSN, "source-dsn", os.Getenv("SOURCE_DSN"), "legacy source database DSN")
	flag.StringVar(&targetDSN, "target-dsn", os.Getenv("TARGET_DSN"), "Stargate target database DSN (defaults to config)")
	flag.BoolVar(&dryRun, "dry-run", false, "read and validate only; do not write")
	flag.IntVar(&batchSize, "batch-size", 500, "rows per insert batch")
	flag.Parse()

	if moduleID == "" {
		log.Fatal("--module is required")
	}
	if sourceDSN == "" {
		log.Fatal("--source-dsn is required")
	}
	if targetDSN == "" {
		cfg, err := config.Load("")
		if err != nil {
			log.Fatalf("load config: %v", err)
		}
		targetDSN = cfg.Database.DSN
	}

	factories := standard.Modules()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	source, err := db.Connect(ctx, sourceDSN)
	if err != nil {
		log.Fatalf("connect source: %v", err)
	}
	defer closeDB(source)

	target, err := db.Connect(ctx, targetDSN)
	if err != nil {
		log.Fatalf("connect target: %v", err)
	}
	defer closeDB(target)

	opts := module.ImportOptions{DryRun: dryRun, BatchSize: batchSize}

	if moduleID == "all" {
		ran := 0
		for _, factory := range factories {
			importer, ok := module.ImporterFor(factories, factory.ID)
			if !ok {
				continue
			}
			ran++
			if err := runOne(ctx, factory, importer, source, target, opts); err != nil {
				log.Fatalf("%s: %v", factory.ID, err)
			}
		}
		if ran == 0 {
			log.Fatal("no modules declare an importer")
		}
		return
	}

	factory, ok := findFactory(factories, moduleID)
	if !ok {
		log.Fatalf("unknown module %q", moduleID)
	}
	importer, ok := module.ImporterFor(factories, moduleID)
	if !ok {
		log.Fatalf("module %q has no importer", moduleID)
	}
	if err := runOne(ctx, factory, importer, source, target, opts); err != nil {
		log.Fatalf("%s: %v", moduleID, err)
	}
}

func runOne(ctx context.Context, factory module.Factory, importer module.Importer, source, target *gorm.DB, opts module.ImportOptions) error {
	if len(factory.Models) > 0 {
		if err := module.MigrateSchema(ctx, target, []module.Factory{factory}); err != nil {
			return fmt.Errorf("prepare target schema: %w", err)
		}
	}
	stats, err := importer.Import(ctx, source, target, opts)
	if err != nil {
		return err
	}
	mode := "imported"
	if opts.DryRun {
		mode = "dry-run"
	}
	if len(stats.Tables) > 1 {
		// A multi-table module reports each table, so a partial skip (for
		// instance a slug already taken) is visible where it happened.
		for _, table := range stats.Tables {
			log.Printf("%s %s %s: read=%d written=%d skipped=%d",
				mode, stats.Module, table.Table, table.Read, table.Written, table.Skipped)
		}
	}
	log.Printf("%s %s: read=%d written=%d skipped=%d", mode, stats.Table, stats.Read, stats.Written, stats.Skipped)
	return nil
}

func findFactory(factories []module.Factory, id string) (module.Factory, bool) {
	for _, factory := range factories {
		if factory.ID == id {
			return factory, true
		}
	}
	return module.Factory{}, false
}

func closeDB(database *gorm.DB) {
	sqlDB, err := database.DB()
	if err == nil {
		_ = sqlDB.Close()
	}
}
