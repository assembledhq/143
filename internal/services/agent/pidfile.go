package agent

import "fmt"

const (
	interruptPIDFileName = ".143-agent.pid"
	interruptTTYFileName = ".143-agent.tty"
)

// InterruptPIDFilePath returns the sandbox-local pidfile used to track the
// current coding-agent process for graceful SIGINT delivery.
func InterruptPIDFilePath(homeDir string) string {
	return fmt.Sprintf("%s/%s", homeDir, interruptPIDFileName)
}

// InterruptTTYFilePath returns the sandbox-local file that records the TTY
// path for agents whose graceful-stop behavior requires keystroke injection.
func InterruptTTYFilePath(homeDir string) string {
	return fmt.Sprintf("%s/%s", homeDir, interruptTTYFileName)
}
