package rendezvous

import (
	"os"
	"os/exec"
	"syscall"
)

// configureProcess ties cloudflared's life to dcc's. If dcc dies without
// stopping the tunnel — a crash, a kill -9, a closed terminal — the kernel
// sends the tunnel the same SIGTERM Stop would have, so nothing is left
// holding a Rendezvous open for a Session that no longer exists.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}

// terminate asks cloudflared to shut down gracefully. It handles SIGTERM
// itself, draining what is in flight before exiting 0.
func terminate(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
