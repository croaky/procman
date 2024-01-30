package main

import (
	"testing"
)

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
			output := &output{}
			mgr := manager{output: output}
			err := mgr.setupProcesses(tt.entries, tt.procNames)

			if (err != nil) != tt.wantError {
				t.Errorf("setupProcesses() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

// TestSetupSignalHandling tests the SetupSignalHandling method of manager
func TestSetupSignalHandling(t *testing.T) {
	output := &output{}
	mgr := manager{output: output, procs: make([]*process, 2)}
	mgr.SetupSignalHandling()

	if mgr.done == nil || mgr.interrupted == nil {
		t.Errorf("SetupSignalHandling did not initialize channels correctly")
	}
}
