package main

import (
	"sync"
	"testing"
)

// mockOutput is a mock implementation of the output for testing purposes
type mockOutput struct {
	mutex         sync.Mutex
	pipes         map[*process]*ptyPipe
	maxNameLength int
}

// NewMockOutput creates a new instance of mockOutput
func NewMockOutput() *mockOutput {
	return &mockOutput{
		pipes: make(map[*process]*ptyPipe),
	}
}

// connect prepares the output for a new process (mocked).
func (mo *mockOutput) connect(proc *process) {
	mo.mutex.Lock()
	defer mo.mutex.Unlock()

	if len(proc.name) > mo.maxNameLength {
		mo.maxNameLength = len(proc.name)
	}

	mo.pipes[proc] = &ptyPipe{} // Mock ptyPipe without actual terminal functionality
}

// pipeOutput handles the output piping for a process (mocked).
func (mo *mockOutput) pipeOutput(proc *process) {
	// Mock implementation, can be empty or log actions for verification in tests
}

// closePipe closes the pseudo-terminal associated with the process (mocked).
func (mo *mockOutput) closePipe(proc *process) {
	// Mock implementation, can be empty or log actions for verification in tests
}

// writeLine writes a line of output for the specified process (mocked).
func (mo *mockOutput) writeLine(proc *process, p []byte) {
	// Mock implementation, can be empty or log actions for verification in tests
}

// writeErr writes an error message for the specified process (mocked).
func (mo *mockOutput) writeErr(proc *process, err error) {
	// Mock implementation, can be empty or log actions for verification in tests
}

// TestSetupProcesses tests the setupProcesses method of manager
func TestSetupProcesses(t *testing.T) {
	tests := []struct {
		name      string
		entries   []entry
		procNames []string
		wantError bool
	}{
		{
			name: "Valid setup",
			entries: []entry{
				{name: "web", cmd: "command1"},
				{name: "db", cmd: "command2"},
			},
			procNames: []string{"web", "db"},
			wantError: false,
		},
		{
			name: "Invalid process name",
			entries: []entry{
				{name: "web", cmd: "command1"},
			},
			procNames: []string{"web", "api"},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockOut := NewMockOutput()
			mgr := manager{output: mockOut}
			err := mgr.setupProcesses(tt.entries, tt.procNames)

			if (err != nil) != tt.wantError {
				t.Errorf("setupProcesses() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

// TestSetupSignalHandling tests the SetupSignalHandling method of manager
func TestSetupSignalHandling(t *testing.T) {
	mockOut := NewMockOutput()
	mgr := manager{output: mockOut, procs: make([]*process, 2)}
	mgr.SetupSignalHandling()

	if mgr.done == nil || mgr.interrupted == nil {
		t.Errorf("SetupSignalHandling did not initialize channels correctly")
	}
}
