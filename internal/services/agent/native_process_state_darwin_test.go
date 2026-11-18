//go:build darwin

package agent

import (
	"bytes"
	"fmt"

	"golang.org/x/sys/unix"
)

// BSD process states as kinfo_proc reports them in extern_proc.p_stat.
const (
	darwinProcessStateStopped = 4 // SSTOP
	darwinProcessStateZombie  = 5 // SZOMB
)

// observeNativeProcess reads the process table entry for pid through the
// kern.proc.pid sysctl, the same source `ps` uses, without spawning anything.
func observeNativeProcess(pid int) (nativeProcessState, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return nativeProcessState{}, fmt.Errorf("read kinfo_proc for %d: %w", pid, err)
	}
	comm := info.Proc.P_comm[:]
	if end := bytes.IndexByte(comm, 0); end >= 0 {
		comm = comm[:end]
	}
	return nativeProcessState{
		Comm:    string(comm),
		Stopped: info.Proc.P_stat == darwinProcessStateStopped,
		Zombie:  info.Proc.P_stat == darwinProcessStateZombie,
	}, nil
}
