// Command audit-review-recipes checks a private pinned inventory without
// executing repository setup. Exit 1 closes the rollout gate; exit 2 means
// the inventory could not be read or validated.
package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"

	"github.com/assembledhq/143/internal/reviewaudit"
	"github.com/rs/zerolog"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	logger := zerolog.New(stderr)
	flags := flag.NewFlagSet("audit-review-recipes", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifest := flags.String("manifest", "", "private version-1 pinned repository inventory JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *manifest == "" || flags.NArg() != 0 {
		logger.Error().Msg("pass --manifest with an explicit private repository inventory")
		return 2
	}
	file, err := os.Open(*manifest)
	if err != nil {
		logger.Error().Err(err).Msg("open recipe audit inventory")
		return 2
	}
	inventory, decodeErr := reviewaudit.DecodeInventory(file)
	if err := file.Close(); err != nil {
		logger.Error().Err(err).Msg("close recipe audit inventory")
		return 2
	}
	if decodeErr != nil {
		logger.Error().Err(decodeErr).Msg("validate recipe audit inventory")
		return 2
	}
	report := reviewaudit.Audit(inventory)
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		logger.Error().Err(err).Msg("write recipe audit report")
		return 2
	}
	if !report.Ready {
		return 1
	}
	return 0
}
