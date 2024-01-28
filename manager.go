package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// manager handles overall process management
type manager struct {
	output      *output
	procs       []*process
	procWg      sync.WaitGroup
	done        chan bool
	interrupted chan os.Signal
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
		go proc.interrupt()
	}

	select {
	case <-time.After(timeout):
	case <-mgr.interrupted:
	}

	for _, proc := range mgr.procs {
		go proc.kill()
	}
}

// setupProcesses creates and initializes processes based on the given entries.
func (mgr *manager) setupProcesses(entries []entry, procNames []string) error {
	mgr.procs = make([]*process, 0)

	for i, name := range procNames {
		cmd := ""
		for _, ent := range entries {
			if name == ent.name {
				cmd = ent.cmd
			}
		}
		if cmd == "" {
			return fmt.Errorf("No process named %s in Procfile.dev\n", name)
		}

		proc := &process{
			Cmd:    exec.Command("/bin/sh", "-c", cmd),
			name:   name,
			color:  colors[i%len(colors)],
			output: mgr.output,
		}
		proc.output.connect(proc)

		mgr.procs = append(mgr.procs, proc)
	}

	return nil
}

// SetupSignalHandling configures handling of interrupt signals.
func (mgr *manager) SetupSignalHandling() {
	mgr.done = make(chan bool, len(mgr.procs))
	mgr.interrupted = make(chan os.Signal)
	signal.Notify(mgr.interrupted, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
}
