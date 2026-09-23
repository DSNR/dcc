//go:build !windows && !linux

package rendezvous

import (
	"os"
	"os/exec"
	"syscall"
)

// configureProcess has nothing to arrange here: only Linux can ask the kernel
// to signal a child when its parent dies, so on any other Unix a crashed dcc
// leaves the tunnel to be reaped by whoever notices. macOS is not a supported
// platform; this file exists so that building on one is not a compile error.
func configureProcess(*exec.Cmd) {}

// terminate asks cloudflared to shut down gracefully.
func terminate(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
