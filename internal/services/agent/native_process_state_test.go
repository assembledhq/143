package agent

// nativeProcessState is a fork-free observation of a child process the review
// workspace tests drive by PID: the executable's short name and whether the
// process is stopped or a zombie. The tests used to poll `ps` for this. Six
// parallel cases each spawning `ps` every millisecond amounted to hundreds of
// process launches per second, enough under full-package load to starve the
// Git child they were waiting on, so the stopped state they asserted arrived
// after the observation window had closed. Reading the kernel's process table
// directly costs microseconds and leaves the scheduler to the child.
type nativeProcessState struct {
	Comm    string
	Stopped bool
	Zombie  bool
}
