package version

import "testing"

func TestIsDev(t *testing.T) {
	t.Parallel()

	// Default value is "dev" so IsDev should return true in tests.
	if !IsDev() {
		t.Error("IsDev should return true when BuildSHA is the default 'dev'")
	}

	// Override BuildSHA and verify IsDev returns false.
	original := BuildSHA
	BuildSHA = "abc123"
	t.Cleanup(func() { BuildSHA = original })
	if IsDev() {
		t.Error("IsDev should return false when BuildSHA is set to a real SHA")
	}
}
