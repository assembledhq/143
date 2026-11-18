//go:build linux

package agent

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// observeNativeProcess parses /proc/<pid>/stat, the same source `ps` uses,
// without spawning anything. The executable name is parenthesised and may
// contain spaces or parentheses, so the state is read after the last ')'.
func observeNativeProcess(pid int) (nativeProcessState, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return nativeProcessState{}, fmt.Errorf("read /proc stat for %d: %w", pid, err)
	}
	stat := string(raw)
	open, end := strings.IndexByte(stat, '('), strings.LastIndexByte(stat, ')')
	if open < 0 || end < open || end+2 >= len(stat) {
		return nativeProcessState{}, fmt.Errorf("unexpected /proc stat for %d: %q", pid, stat)
	}
	state := stat[end+2]
	return nativeProcessState{
		Comm:    stat[open+1 : end],
		Stopped: state == 'T' || state == 't',
		Zombie:  state == 'Z',
	}, nil
}
