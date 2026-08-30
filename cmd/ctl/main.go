package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/mss-boot-io/mss-knowledge/internal/buildinfo"
	"github.com/mss-boot-io/mss-knowledge/internal/config"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "mss-knowledge-ctl: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		printUsage()
		return nil
	}

	switch arguments[0] {
	case "version":
		return writeJSON(buildinfo.Current())
	case "config":
		if len(arguments) != 2 || arguments[1] != "check" {
			return fmt.Errorf("usage: mss-knowledge-ctl config check")
		}
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("configuration is invalid: %w", err)
		}
		return writeJSON(map[string]any{
			"status":      "valid",
			"service":     cfg.ServiceName,
			"environment": cfg.Environment,
			"http_address": cfg.HTTP.Address,
		})
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q", arguments[0])
	}
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func printUsage() {
	_, _ = fmt.Fprintln(os.Stdout, `mss-knowledge-ctl

Usage:
  mss-knowledge-ctl version
  mss-knowledge-ctl config check

Planned commands:
  mss-knowledge-ctl index rebuild --all
  mss-knowledge-ctl index rebuild --knowledge-base <id>
  mss-knowledge-ctl document reindex --document <id>`)
}
