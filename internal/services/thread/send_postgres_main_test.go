package thread

import (
	"os"
	"testing"

	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
)

func TestMain(m *testing.M) { os.Exit(testpostgres.Run(m)) }
