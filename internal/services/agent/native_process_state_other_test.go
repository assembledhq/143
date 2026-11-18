//go:build !darwin && !linux

package agent

import "errors"

// observeNativeProcess is only implemented where the review workspace tests
// run (macOS development machines and Linux CI).
func observeNativeProcess(int) (nativeProcessState, error) {
	return nativeProcessState{}, errors.New("native process observation is not implemented on this platform")
}
