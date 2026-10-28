// messager-migrate copies the legacy DysonNetwork.Messager database into a
// Lighthouse Messager database.
//
// Order matters: boot the service once so the schema exists (module schema
// hooks create the tables), then run this command, then restart the service.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"src.solsynth.dev/sosys/lighthouse/internal/messager/migrate"
	"src.solsynth.dev/sosys/lighthouse/internal/platform/legacyimport"
)

func main() {
	source := flag.String("source-dsn", os.Getenv("MESSAGER_SOURCE_DSN"), "legacy DysonNetwork.Messager PostgreSQL DSN")
	target := flag.String("target-dsn", os.Getenv("MESSAGER_TARGET_DSN"), "Lighthouse Messager PostgreSQL DSN")
	dryRun := flag.Bool("dry-run", false, "report rows without writing")
	flag.Parse()

	if *source == "" || *target == "" {
		fmt.Fprintln(os.Stderr, "messager-migrate: --source-dsn and --target-dsn are required")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	report, err := legacyimport.Run(ctx, *source, *target, migrate.Tables, *dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "messager-migrate: %v\n", err)
		os.Exit(1)
	}
	for _, table := range report.Tables {
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
