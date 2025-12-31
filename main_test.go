package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	cmd := exec.Command("go", "build", "-o", "procman", ".")
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	os.Remove("procman")
	os.Remove("Procfile.dev")
	os.Exit(code)
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    []string
		wantErr bool
	}{
		{
			name:    "No arguments",
			args:    []string{"procman"},
			want:    nil,
			wantErr: true,
		},
		{
			name:    "Single process",
			args:    []string{"procman", "web"},
			want:    []string{"web"},
			wantErr: false,
		},
		{
			name:    "Multiple processes",
			args:    []string{"procman", "web,db,cache"},
			want:    []string{"web", "db", "cache"},
			wantErr: false,
		},
		{
			name:    "Extra whitespace",
			args:    []string{"procman", "web, db , cache "},
			want:    []string{"web", "db", "cache"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseArgs() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseProcfile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []procDef
		wantErr bool
	}{
		{
			name:    "Valid procfile",
			content: "web: npm start\nworker: npm run worker",
			want: []procDef{
				{name: "web", cmd: "npm start"},
				{name: "worker", cmd: "npm run worker"},
			},
			wantErr: false,
		},
		{
			name:    "Empty procfile",
			content: "",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "Invalid format",
			content: "web npm start",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "Duplicate names",
			content: "web: npm start\nweb: npm run something",
			want:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseProcfile(strings.NewReader(tt.content))

			if (err != nil) != tt.wantErr {
				t.Errorf("parseProcfile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseProcfile() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSetupProcesses(t *testing.T) {
	tests := []struct {
		name      string
		defs      []procDef
		procNames []string
		wantError bool
	}{
		{
			name: "Valid setup",
			defs: []procDef{
				{name: "web", cmd: "command1"},
				{name: "db", cmd: "command2"},
			},
			procNames: []string{"web", "db"},
			wantError: false,
		},
		{
			name: "Invalid process name",
			defs: []procDef{
				{name: "web", cmd: "command1"},
			},
			procNames: []string{"web", "api"},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := manager{output: &output{}}
			err := mgr.setupProcesses(tt.defs, tt.procNames)

			if (err != nil) != tt.wantError {
				t.Errorf("setupProcesses() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

func TestSetupSignalHandling(t *testing.T) {
	mgr := manager{output: &output{}, procs: make([]*process, 2)}
	mgr.setupSignalHandling()

	if mgr.done == nil || mgr.interrupted == nil {
		t.Errorf("setupSignalHandling did not initialize channels correctly")
	}
}

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

func TestInit(t *testing.T) {
	out := &output{}
	procs := []*process{{name: "testProcess"}}

	out.init(procs)

	if out.maxNameLength != len(procs[0].name) {
		t.Errorf("Expected maxNameLength to be %d, got %d", len(procs[0].name), out.maxNameLength)
	}
	if out.pipes == nil {
		t.Errorf("Expected pipes to be initialized")
	}
}

func TestWriteLine(t *testing.T) {
	out := &output{maxNameLength: 11} // Length of "testProcess"
	proc := &process{name: "testProcess", color: 31}

	// Capture standard output
	originalStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	out.writeLine(proc, []byte("Hello world"))

	w.Close()
	os.Stdout = originalStdout

	var buf bytes.Buffer
	buf.ReadFrom(r)

	expected := "\033[1;38;5;31mtestProcess \033[0m| Hello world\n"
	if got := buf.String(); got != expected {
		t.Errorf("Expected output %q, got %q", expected, got)
	}
}

func TestWriteErr(t *testing.T) {
	out := &output{maxNameLength: 11}
	proc := &process{name: "testProcess", color: 31}
	testErr := fmt.Errorf("test error")

	originalStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	out.writeErr(proc, testErr)

	w.Close()
	os.Stdout = originalStdout

	var buf bytes.Buffer
	buf.ReadFrom(r)

	expected := "\033[1;38;5;31mtestProcess \033[0m| \033[0;31mtest error\033[0m\n"
	if got := buf.String(); got != expected {
		t.Errorf("Expected output %q, got %q", expected, got)
	}
}

// TestProcmanIntegration tests the full procman workflow.
func TestProcmanIntegration(t *testing.T) {
	content := "echo: echo 'hello'\nsleep: sleep 10"
	if err := os.WriteFile("Procfile.dev", []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create mock Procfile.dev: %v", err)
	}
	defer os.Remove("Procfile.dev")

	cmd := exec.Command("./procman", "echo,sleep")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start procman: %v", err)
	}

	time.Sleep(1 * time.Second)

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("Failed to send SIGINT to procman: %v", err)
	}

	if err := cmd.Wait(); err != nil {
		t.Fatalf("procman did not exit cleanly: %v", err)
	}
}
