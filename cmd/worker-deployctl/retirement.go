package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/db"
)

func parseSealRetirementArgs(args []string) (db.SealNodeRetirementParams, bool, error) {
	fs := flag.NewFlagSet("seal-retirement", flag.ContinueOnError)
	var params db.SealNodeRetirementParams
	fs.StringVar(&params.NodeID, "node-id", "", "exact worker generation to retire")
	fs.StringVar(&params.DeployID, "deploy-id", "", "exact planned deployment attempt")
	fs.StringVar(&params.BuildSHA, "build-sha", "", "deployment build SHA")
	jsonOut := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return db.SealNodeRetirementParams{}, false, err
	}
	if fs.NArg() != 0 || strings.TrimSpace(params.NodeID) == "" || strings.TrimSpace(params.DeployID) == "" || strings.TrimSpace(params.BuildSHA) == "" {
		return db.SealNodeRetirementParams{}, false, fmt.Errorf("seal-retirement requires --node-id, --deploy-id and --build-sha")
	}
	return params, *jsonOut, nil
}

type retirementSealer interface {
	Seal(context.Context, db.SealNodeRetirementParams) (db.NodeRetirementSeal, error)
}

func sealRetirement(ctx context.Context, store retirementSealer, args []string) (db.NodeRetirementSeal, bool, int, error) {
	params, jsonOut, err := parseSealRetirementArgs(args)
	if err != nil {
		return db.NodeRetirementSeal{}, false, 1, err
	}
	seal, err := store.Seal(ctx, params)
	if errors.Is(err, db.ErrNodeRetirementBusy) {
		return db.NodeRetirementSeal{}, jsonOut, 3, err
	}
	if err != nil {
		return db.NodeRetirementSeal{}, jsonOut, 1, err
	}
	return seal, jsonOut, 0, nil
}
