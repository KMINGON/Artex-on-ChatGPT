//go:build !windows

package server

import (
	"os"
	"os/exec"
	"syscall"
)

func isolateBrowserProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func killBrowserProcess(p *os.Process) {
	if syscall.Kill(-p.Pid, syscall.SIGKILL) != nil {
		_ = p.Kill()
	}
}
