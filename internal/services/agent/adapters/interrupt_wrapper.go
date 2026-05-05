package adapters

import (
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/services/agent"
)

// wrapCommandForInterruptTracking launches the CLI under the runtime control
// wrapper needed for the requested graceful-stop method.
func wrapCommandForInterruptTracking(homeDir string, spec agent.CancellationSpec, cmd string) string {
	switch spec.Method {
	case agent.CancellationMethodEscape:
		return wrapCommandForEscape(homeDir, cmd)
	default:
		return wrapCommandForCtrlC(homeDir, cmd)
	}
}

func wrapCommandForCtrlC(homeDir, cmd string) string {
	pidFile := shellEscapeSingle(agent.InterruptPIDFilePath(homeDir))
	return fmt.Sprintf("(%s) & pid=$!; printf '%%s\\n' \"$pid\" > '%s'; wait \"$pid\"", cmd, pidFile)
}

func wrapCommandForEscape(homeDir, cmd string) string {
	pidFile := agent.InterruptPIDFilePath(homeDir)
	ttyFile := agent.InterruptTTYFilePath(homeDir)
	python := strings.Join([]string{
		"import os, pty, subprocess, sys",
		"cmd, pid_file, tty_file = sys.argv[1:4]",
		"master_fd, slave_fd = pty.openpty()",
		"tty_path = os.ttyname(slave_fd)",
		"with open(tty_file, 'w', encoding='utf-8') as f: f.write(tty_path)",
		"proc = subprocess.Popen(['/bin/sh', '-lc', cmd], stdin=slave_fd, stdout=slave_fd, stderr=slave_fd, close_fds=True)",
		"os.close(slave_fd)",
		"with open(pid_file, 'w', encoding='utf-8') as f: f.write(str(proc.pid))",
		"try:",
		"    while True:",
		"        try:",
		"            chunk = os.read(master_fd, 4096)",
		"        except OSError:",
		"            break",
		"        if not chunk:",
		"            break",
		"        os.write(1, chunk)",
		"finally:",
		"    os.close(master_fd)",
		"sys.exit(proc.wait())",
	}, "; ")
	return fmt.Sprintf(
		"python3 -c '%s' '%s' '%s' '%s'",
		shellEscapeSingle(python),
		shellEscapeSingle(cmd),
		shellEscapeSingle(pidFile),
		shellEscapeSingle(ttyFile),
	)
}
