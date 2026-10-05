package rendezvous

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// configureProcess gives cloudflared a console process group of its own. That
// is what makes a CTRL_BREAK deliverable to it and to nothing else: a
// CTRL_C cannot be aimed at one group, and would stop dcc along with the
// tunnel.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// terminate asks cloudflared to shut down gracefully. Windows has no SIGTERM;
// a console control event is the only graceful trigger there is, and
// cloudflared takes its own graceful path when it arrives. When it cannot be
// delivered — dcc-gui has no console to share — the caller kills the process
// instead, which a tunnel with nothing on disk survives being asked to do.
func terminate(p *os.Process) error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(p.Pid))
}
