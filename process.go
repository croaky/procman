package main

import (
	"os"
	"os/exec"
	"syscall"
)

// process represents an individual process to be managed
type process struct {
	*exec.Cmd

	name   string
	color  int
	output Outputter
}

// running inspects the process to see if it is currently running.
func (proc *process) running() bool {
	return proc.Process != nil && proc.ProcessState == nil
}

// run starts the execution of the process and handles its output.
func (proc *process) run() {
	proc.output.pipeOutput(proc)
	defer proc.output.closePipe(proc)

	if err := proc.Cmd.Run(); err != nil {
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
func (proc *process) signal(sig os.Signal) {
	group, err := os.FindProcess(-proc.Process.Pid)
	if err != nil {
		proc.output.writeErr(proc, err)
		return
	}

	if err = group.Signal(sig); err != nil {
		proc.output.writeErr(proc, err)
	}
}
