package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

const timeout = 5 * time.Second

// manager handles overall process management
type manager struct {
	output      *output
	procs       []*process
	procWg      sync.WaitGroup
	done        chan bool
	interrupted chan os.Signal
}

// setupProcesses creates and initializes processes based on the given procDefs.
func (mgr *manager) setupProcesses(defs []procDef, procNames []string) error {
	defMap := make(map[string]string)
	for _, def := range defs {
		defMap[def.name] = def.cmd
	}

	for i, name := range procNames {
		cmd, ok := defMap[name]
		if !ok {
			return fmt.Errorf("No process named %s in Procfile.dev\n", name)
		}

		proc := &process{
			Cmd:    exec.Command("/bin/sh", "-c", cmd),
			name:   name,
			color:  colors[i%len(colors)],
			output: mgr.output,
		}
		mgr.procs = append(mgr.procs, proc)
	}
	return nil
}

// setupSignalHandling configures handling of interrupt signals.
func (mgr *manager) setupSignalHandling() {
	mgr.done = make(chan bool, len(mgr.procs))
	mgr.interrupted = make(chan os.Signal, 1)
	signal.Notify(mgr.interrupted, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
}

// runProcess adds the process to the wait group and starts it.
func (mgr *manager) runProcess(proc *process) {
	mgr.procWg.Add(1)
	go func() {
		defer mgr.procWg.Done()
		defer func() { mgr.done <- true }()
		proc.run()
	}()
}

// waitForExit waits for all processes to exit or for an interruption signal.
func (mgr *manager) waitForExit() {
	select {
	case <-mgr.done:
	case <-mgr.interrupted:
	}

	for _, proc := range mgr.procs {
		proc.interrupt()
	}

	select {
	case <-time.After(timeout):
	case <-mgr.interrupted:
	}

	for _, proc := range mgr.procs {
		proc.kill()
	}
}

// process represents an individual process to be managed
type process struct {
	*exec.Cmd
	name   string
	color  int
	output *output
}

// running inspects the process to see if it is currently running.
func (proc *process) running() bool {
	return proc.Process != nil && proc.ProcessState == nil
}

// run starts the execution of the process and handles its output.
func (proc *process) run() {
	if proc.Process != nil {
		fmt.Fprintf(os.Stderr, "Process %s already started\n", proc.name)
		return
	}

	proc.output.pipeOutput(proc)
	defer proc.output.closePipe(proc)

	if err := proc.Cmd.Wait(); err != nil {
		proc.output.writeErr(proc, err)
	}
}

// interrupt sends an interrupt signal to a running process.
func (proc *process) interrupt() {
	if proc.running() {
		proc.signal(syscall.SIGINT)
	}
}

// kill forcefully stops a running process.
func (proc *process) kill() {
	if proc.running() {
		proc.signal(syscall.SIGKILL)
	}
}

// signal sends a specified signal to the process group.
func (proc *process) signal(sig syscall.Signal) {
	if err := syscall.Kill(-proc.Process.Pid, sig); err != nil {
		proc.output.writeErr(proc, err)
	}
}

// output manages the output display of processes
type output struct {
	maxNameLength int
	mutex         sync.Mutex
	pipes         map[*process]*os.File
}

// init initializes the output handler for all processes.
func (out *output) init(procs []*process) {
	out.pipes = make(map[*process]*os.File)
	for _, proc := range procs {
		if len(proc.name) > out.maxNameLength {
			out.maxNameLength = len(proc.name)
		}
	}
}

// pipeOutput handles the output piping for a process.
func (out *output) pipeOutput(proc *process) {
	ptyFile, err := pty.Start(proc.Cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening PTY: %v\n", err)
		os.Exit(1)
	}

	out.mutex.Lock()
	out.pipes[proc] = ptyFile
	out.mutex.Unlock()

	go func() {
		scanner := bufio.NewScanner(ptyFile)
		for scanner.Scan() {
			out.writeLine(proc, scanner.Bytes())
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
			out.writeErr(proc, err)
		}
	}()
}

// closePipe closes the pseudo-terminal associated with the process.
func (out *output) closePipe(proc *process) {
	out.mutex.Lock()
	ptyFile := out.pipes[proc]
	out.mutex.Unlock()

	if ptyFile != nil {
		ptyFile.Close()
	}
}

// writeLine writes a line of output for the specified process, with color formatting.
func (out *output) writeLine(proc *process, p []byte) {
	var buf bytes.Buffer
	buf.WriteString(fmt.Sprintf("\033[1;38;5;%vm", proc.color))
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
	out.writeLine(proc, []byte(fmt.Sprintf("\033[0;31m%v\033[0m", err)))
}
