package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	githubtelemetry "github.com/assembledhq/143/internal/services/github/telemetry"
)

const maxGitHubJSONResponseBytes = 10 << 20

// decodeGitHubJSONResponse validates one complete bounded JSON document before
// reporting probe recovery. A decoder can otherwise return before body EOF.
func decodeGitHubJSONResponse(ctx context.Context, body io.Reader, target any) (err error) {
	defer func() {
		err = errors.Join(err, ctx.Err())
		githubtelemetry.ObserveJSONResponse(ctx, err)
	}()
	limited := &io.LimitedReader{R: body, N: maxGitHubJSONResponseBytes + 1}
	decoder := json.NewDecoder(limited)
	if err = decoder.Decode(target); err != nil {
		return err
	}
	// Consume only whitespace after the first value, including bytes already
	// buffered by the decoder. Never allocate or accept a second JSON value.
	remaining := io.MultiReader(decoder.Buffered(), limited)
	var buffer [4096]byte
	for {
		n, readErr := remaining.Read(buffer[:])
		if limited.N == 0 {
			return errors.New("GitHub JSON response exceeded size limit")
		}
		for _, b := range buffer[:n] {
			if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
				return errors.New("GitHub JSON response contains trailing content")
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}
