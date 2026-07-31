package cli

import (
	"os"
	"strings"

	"github.com/assembledhq/143/internal/internalapi"
)

func codingSessionIDFromEnv() string {
	return strings.TrimSpace(os.Getenv(internalapi.CodingSessionIDEnvVar))
}
