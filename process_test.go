package main

import (
	"os/exec"
	"testing"
)

// TestProcessRunning tests the running state of a process
func TestProcessRunning(t *testing.T) {
	cmd := exec.Command("sleep", "1")
	proc := &process{Cmd: cmd, output: &output{}}

	if proc.running() {
		t.Errorf("expected process to not be running before start")
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start process: %v", err)
	}

	if !proc.running() {
		t.Errorf("expected process to be running after start")
	}
}
