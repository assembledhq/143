package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// readRepositoryFile confines manifest and discovered source reads to the
// selected repository root, including when a source file is a symbolic link.
// path includes root, as supplied by the walk, glob, or manifest join.
func readRepositoryFile(root, path string) ([]byte, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, fmt.Errorf("resolve repository file: %w", err)
	}
	repository, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open repository root: %w", err)
	}
	data, readErr := repository.ReadFile(rel)
	closeErr := repository.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("read repository file: %w", err)
	}
	return data, nil
}
