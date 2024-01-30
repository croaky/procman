package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestConnect tests the connect method of the output struct
func TestConnect(t *testing.T) {
	out := &output{}
	proc := &process{name: "testProcess"}

	out.connect(proc)

	if _, exists := out.pipes[proc]; !exists {
		t.Errorf("Expected process to be in pipes map")
	}

	if out.maxNameLength != len(proc.name) {
		t.Errorf("Expected maxNameLength to be %d, got %d", len(proc.name), out.maxNameLength)
	}
}

// TestWriteLine tests the writeLine method of the output struct
func TestWriteLine(t *testing.T) {
	out := &output{maxNameLength: 11} // Length of "testProcess"
	proc := &process{name: "testProcess", color: 31}

	// Capture standard output
	originalStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	out.writeLine(proc, []byte("Hello world"))

	// Restore original stdout
	w.Close()
	os.Stdout = originalStdout

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	expectedOutput := "\033[1;38;5;31mtestProcess \033[0m| Hello world\n"
	if !strings.Contains(output, expectedOutput) {
		t.Errorf("Expected output to contain %q, got %q", expectedOutput, output)
	}
}

// TestWriteErr tests the writeErr method of the output struct
func TestWriteErr(t *testing.T) {
	out := &output{maxNameLength: 11} // Length of "testProcess"
	proc := &process{name: "testProcess", color: 31}
	testErr := fmt.Errorf("test error")

	// Capture standard output
	originalStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	out.writeErr(proc, testErr)

	// Restore original stdout
	w.Close()
	os.Stdout = originalStdout

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	expectedOutput := "\033[1;38;5;31mtestProcess \033[0m| \033[0;31mtest error\033[0m\n"
	if !strings.Contains(output, expectedOutput) {
		t.Errorf("Expected output to contain %q, got %q", expectedOutput, output)
	}
}
