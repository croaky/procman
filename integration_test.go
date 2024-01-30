package main

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestProcmanIntegration(t *testing.T) {
	// Create a mock Procfile.dev with simple Unix commands
	if err := createMockProcfileDev(); err != nil {
		t.Fatalf("Failed to create mock Procfile.dev: %v", err)
	}
	defer deleteMockProcfileDev()

	// Start procman with the mock processes
	cmd := exec.Command("./procman", "echo,sleep")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start procman: %v", err)
	}

	// Allow some time for procman to start processes
	time.Sleep(1 * time.Second)

	// Send a SIGINT to procman to simulate user interruption
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("Failed to send SIGINT to procman: %v", err)
	}

	// Wait for procman to exit
	if err := cmd.Wait(); err != nil {
		t.Fatalf("procman did not exit cleanly: %v", err)
	}
}

func createMockProcfileDev() error {
	content := "echo: echo 'Hello from Procman'\nsleep: sleep 10"
	return os.WriteFile("Procfile.dev", []byte(content), 0644)
}

func deleteMockProcfileDev() {
	os.Remove("Procfile.dev") // clean up the mock Procfile.dev
}
