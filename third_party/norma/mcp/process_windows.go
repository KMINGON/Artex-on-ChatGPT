//go:build windows

package mcp

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func isolateProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000200}
}

func killProcessTree(p *os.Process) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid)).Run()
	_ = p.Kill()
}
