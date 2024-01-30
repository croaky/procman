package main

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// Ensure `go build -o procman .` runs before running integration tests

// TestProcmanIntegration tests that all key parts of procman are working
// correctly such as Procfile.dev parsing, argument parsing, command executing,
// and signal handling.
func TestProcmanIntegration(t *testing.T) {
	content := "echo: echo 'Hello from Procman'\nsleep: sleep 10"
	err := os.WriteFile("Procfile.dev", []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to create mock Procfile.dev: %v", err)
	}
	defer os.Remove("Procfile.dev") // clean up the mock

	cmd := exec.Command("./procman", "echo,sleep")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start procman: %v", err)
	}

	// Allow some time for procman to start processes
	time.Sleep(1 * time.Second)

	// Simulate user interruption
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("Failed to send SIGINT to procman: %v", err)
	}

	// Wait for procman to exit
	if err := cmd.Wait(); err != nil {
		t.Fatalf("procman did not exit cleanly: %v", err)
	}
}
