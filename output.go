package main

import (
	"bytes"
	"fmt"
	"os"
	"sync"

	"github.com/creack/pty"
)

// output manages the output display of processes
type output struct {
	maxNameLength int
	mutex         sync.Mutex
	pipes         map[*process]*ptyPipe
}

// ptyPipe is used for managing pseudo-terminal (PTY) devices
type ptyPipe struct {
	pty, tty *os.File
}

// connect prepares the output for a new process.
func (out *output) connect(proc *process) {
	if len(proc.name) > out.maxNameLength {
		out.maxNameLength = len(proc.name)
	}

	if out.pipes == nil {
		out.pipes = make(map[*process]*ptyPipe)
	}

	out.pipes[proc] = &ptyPipe{}
}

// pipeOutput handles the output piping for a process.
func (out *output) pipeOutput(proc *process) {
	pipe := out.openPipe(proc)

	go func(proc *process, pipe *ptyPipe) {
		scanLines(pipe.pty, func(b []byte) bool {
			out.writeLine(proc, b)
			return true
		})
	}(proc, pipe)
}

// openPipe initializes a pseudo-terminal and starts the given process.
func (out *output) openPipe(proc *process) (pipe *ptyPipe) {
	var err error
	pipe = out.pipes[proc]

	pipe.pty, err = pty.Start(proc.Cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening PTY: %v\n", err)
		os.Exit(1)
	}

	return
}

// closePipe closes the pseudo-terminal associated with the process.
func (out *output) closePipe(proc *process) {
	if pipe := out.pipes[proc]; pipe != nil {
		pipe.pty.Close()
		pipe.tty.Close()
	}
}

// writeLine writes a line of output for the specified process, with color formatting.
func (out *output) writeLine(proc *process, p []byte) {
	var buf bytes.Buffer
	color := fmt.Sprintf("\033[1;38;5;%vm", proc.color)

	buf.WriteString(color)
	buf.WriteString(proc.name)

	for i := len(proc.name); i <= out.maxNameLength; i++ {
		buf.WriteByte(' ')
	}

	buf.WriteString("\033[0m| ")
	buf.Write(p)
	buf.WriteByte('\n')

	out.mutex.Lock()
	defer out.mutex.Unlock()

	buf.WriteTo(os.Stdout)
}

// writeErr writes an error message for the specified process.
func (out *output) writeErr(proc *process, err error) {
	out.writeLine(proc, []byte(
		fmt.Sprintf("\033[0;31m%v\033[0m", err),
	))
}
