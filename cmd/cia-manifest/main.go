package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sitr3n/local-ai-provider/internal/manifestvalidator"
)

func main() {
	if err := run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "cia-manifest: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	flags := flag.NewFlagSet("cia-manifest", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	schemaPath := flags.String("schema", "", "path to models.schema.json")
	manifestPath := flags.String("manifest", "", "path to the JSON-compatible models.yaml")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *schemaPath == "" || *manifestPath == "" {
		return fmt.Errorf("--schema and --manifest are required")
	}
	schemaData, err := os.ReadFile(filepath.Clean(*schemaPath))
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	manifestData, err := os.ReadFile(filepath.Clean(*manifestPath))
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	return manifestvalidator.Validate(schemaData, manifestData)
}
