// sphere-migrate copies the legacy DysonNetwork.Sphere database into a
// Lighthouse Sphere database.
//
// Order matters: boot the service once so the schema exists (module schema
// hooks create the tables), then run this command, then restart the service so
// seeds and backfills see the imported rows.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"src.solsynth.dev/sosys/lighthouse/internal/platform/legacyimport"
	"src.solsynth.dev/sosys/lighthouse/internal/sphere/migrate"
)

func main() {
	source := flag.String("source-dsn", os.Getenv("SPHERE_SOURCE_DSN"), "legacy DysonNetwork.Sphere PostgreSQL DSN")
	target := flag.String("target-dsn", os.Getenv("SPHERE_TARGET_DSN"), "Lighthouse Sphere PostgreSQL DSN")
	dryRun := flag.Bool("dry-run", false, "report rows without writing")
	flag.Parse()

	if *source == "" || *target == "" {
		fmt.Fprintln(os.Stderr, "sphere-migrate: --source-dsn and --target-dsn are required")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	report, err := legacyimport.Run(ctx, *source, *target, migrate.Tables, *dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sphere-migrate: %v\n", err)
		os.Exit(1)
	}
	for _, table := range report.Tables {
		if len(table.MissingInSource) > 0 {
			fmt.Printf("%-32s %8d rows (target-only columns keep defaults: %s)\n",
				table.Table, table.Written, strings.Join(table.MissingInSource, ", "))
			continue
		}
		fmt.Printf("%-32s %8d rows\n", table.Table, table.Written)
	}
	for _, skipped := range report.Skipped {
		fmt.Printf("skipped: %s\n", skipped)
	}
	mode := "imported"
	if *dryRun {
		mode = "would import"
	}
	fmt.Printf("%s %d rows across %d tables\n", mode, report.TotalWritten(), len(report.Tables))
}
