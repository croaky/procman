package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/term/termios"
)

var (
	colors   = []int{2, 3, 4, 5, 6, 42, 130, 103, 129, 108}
	procfile = "./Procfile.dev"
	timeout  = time.Duration(5) * time.Second
)

// entry represents a single line in the procfile, with a name and command
type entry struct {
	name string
	cmd  string
}

// manager handles overall process management
type manager struct {
	output      *output
	procs       []*process
	procWg      sync.WaitGroup
	done        chan bool
	interrupted chan os.Signal
}

// process represents an individual process to be managed
type process struct {
	*exec.Cmd

	name   string
	color  int
	output *output
}

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

// check checks for an error and exits the program if an error is found
func check(err error) {
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func main() {
	procNames, err := parseArgs(os.Args)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	mgr := &manager{}
	mgr.output = &output{}

	file, err := os.Open(procfile)
	check(err)
	defer file.Close()

	re, _ := regexp.Compile(`^([\w-]+):\s+(.+)$`)
	names := make(map[string]bool)
	var entries []entry

	err = scanLines(file, func(b []byte) bool {
		if len(b) == 0 {
			return true
		}

		params := re.FindStringSubmatch(string(b))
		if len(params) != 3 {
			return true
		}

		name, cmd := params[1], params[2]
		if names[name] {
			fmt.Printf("Duplicate process name %s in Procfile.dev", name)
			os.Exit(1)
		}
		names[name] = true
		entries = append(entries, entry{name: name, cmd: cmd})

		return true
	})
	check(err)

	if len(entries) == 0 {
		fmt.Println("No entries found in Procfile.dev")
		os.Exit(1)
	}

	mgr.procs = make([]*process, 0)

	for i, name := range procNames {
		cmd := ""
		for _, ent := range entries {
			if name == ent.name {
				cmd = ent.cmd
			}
		}
		if cmd == "" {
			fmt.Printf("No process named %s in Procfile.dev\n", name)
			os.Exit(1)
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

	mgr.done = make(chan bool, len(mgr.procs))

	mgr.interrupted = make(chan os.Signal)
	signal.Notify(mgr.interrupted, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	for _, proc := range mgr.procs {
		mgr.runProcess(proc)
	}

	go mgr.waitForExit()

	mgr.procWg.Wait()
}

// parseArgs parses command-line arguments and returns a list of process names.
func parseArgs(args []string) ([]string, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("no processes given as arguments")
	}
	var procNames []string
	split := strings.Split(args[1], ",")
	for _, s := range split {
		s = strings.Trim(s, " ")
		if len(s) > 0 {
			procNames = append(procNames, s)
		}
	}
	return procNames, nil
}

// scanLines reads lines from an input reader and applies a callback function to
// each line. It is used for processing the contents of the procfile.
func scanLines(r io.Reader, callback func([]byte) bool) error {
	var (
		err      error
		line     []byte
		isPrefix bool
	)

	reader := bufio.NewReader(r)
	buf := new(bytes.Buffer)

	for {
		line, isPrefix, err = reader.ReadLine()
		if err != nil {
			break
		}

		buf.Write(line)

		if !isPrefix {
			if !callback(buf.Bytes()) {
				return nil
			}
			buf.Reset()
		}
	}
	if err != io.EOF && err != io.ErrClosedPipe {
		return err
	}
	return nil
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

// running checks if the process is currently running.
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

// openPipe initializes a pseudo-terminal for the given process.
func (out *output) openPipe(proc *process) (pipe *ptyPipe) {
	var err error
	pipe = out.pipes[proc]

	pipe.pty, pipe.tty, err = termios.Pty()
	check(err)

	proc.Stdout = pipe.tty
	proc.Stderr = pipe.tty
	proc.Stdin = pipe.tty
	proc.SysProcAttr = &syscall.SysProcAttr{Setctty: true, Setsid: true}

	return
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
