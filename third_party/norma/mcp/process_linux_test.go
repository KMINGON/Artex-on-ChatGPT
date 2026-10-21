//go:build linux

package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMCPProcessHelper(t *testing.T) {
	mode := os.Getenv("NORMA_MCP_HELPER")
	if mode == "" {
		return
	}
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(os.Getenv("NORMA_MCP_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
		os.Exit(3)
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		json.Unmarshal(sc.Bytes(), &req)
		if req.Method != "initialize" {
			continue
		}
		if mode == "cancel" {
			continue
		}
		if mode == "fail" {
			fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"error\":{\"code\":-32603,\"message\":\"fixture failure\"}}\n", req.ID)
		} else {
			fmt.Printf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"protocolVersion\":\"2024-11-05\"}}\n", req.ID)
		}
	}
	child.Wait()
	os.Exit(0)
}

func processRunning(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	parts := strings.SplitN(string(b), ") ", 2)
	return len(parts) != 2 || !strings.HasPrefix(parts[1], "Z")
}

func TestStdioCloseKillsOnlyItsProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep unavailable")
	}
	sibling := exec.Command("sleep", "30")
	if err := sibling.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { sibling.Process.Kill(); sibling.Wait() }()
	for _, mode := range []string{"normal", "fail", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			pidfile := filepath.Join(t.TempDir(), "pid")
			ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
			defer cancel()
			c, err := NewStdioClient(ctx, "fixture", os.Args[0], map[string]string{"NORMA_MCP_HELPER": mode, "NORMA_MCP_PID": pidfile}, "-test.run=^TestMCPProcessHelper$")
			if mode == "normal" {
				if err != nil {
					t.Fatal(err)
				}
				c.Close()
				c.Close()
			} else if err == nil {
				c.Close()
				t.Fatal("expected initialize failure")
			}
			b, err := os.ReadFile(pidfile)
			if err != nil {
				t.Fatal(err)
			}
			pid, _ := strconv.Atoi(string(b))
			defer syscall.Kill(pid, syscall.SIGKILL)
			deadline := time.Now().Add(time.Second)
			for processRunning(pid) && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			if processRunning(pid) {
				t.Fatal("MCP child survived cleanup")
			}
			if !processRunning(sibling.Process.Pid) {
				t.Fatal("unrelated process was killed")
			}
		})
	}
}
