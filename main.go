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

	"github.com/joho/godotenv"
	"github.com/pkg/term/termios"
)

var colors = []int{2, 3, 4, 5, 6, 42, 130, 103, 129, 108}

type manager struct {
	title       string
	output      *output
	procs       []*process
	procWg      sync.WaitGroup
	done        chan bool
	interrupted chan os.Signal
	timeout     time.Duration
}

type process struct {
	*exec.Cmd

	Name  string
	Color int

	output *output
}

type output struct {
	maxNameLength int
	mutex         sync.Mutex
	pipes         map[*process]*ptyPipe
}

type ptyPipe struct {
	pty, tty *os.File
}

type entry struct {
	Name    string
	Command string
	Port    int
}

func check(err error) {
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func main() {
	err := godotenv.Load()
	if err != nil {
		fmt.Println("Error loading .env file")
		os.Exit(1)
	}

	if len(os.Args) < 2 {
		fmt.Println("No processes given as arguments")
		os.Exit(1)
	}
	var procNames []string
	split := strings.Split(os.Args[1], ",")

	for _, s := range split {
		s = strings.Trim(s, " ")
		if len(s) > 0 {
			procNames = append(procNames, s)
		}
	}

	mgr := &manager{timeout: time.Duration(5) * time.Second}
	mgr.output = &output{}

	var entries []entry
	file, err := os.Open("./Procfile.dev")
	check(err)
	defer file.Close()

	re, _ := regexp.Compile(`^([\w-]+):\s+(.+)$`)
	port := 5000
	names := make(map[string]bool)

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

		entries = append(entries, entry{name, cmd, port})
		port += 100

		return true
	})
	check(err)

	if len(entries) == 0 {
		fmt.Println("No entries found in Procfile.dev")
		os.Exit(1)
	}

	mgr.procs = make([]*process, 0)

	for _, name := range procNames {
		l := len(mgr.procs)
		for i, entry := range entries {
			if name == entry.Name {
				mgr.procs = append(mgr.procs, newProcess(entry.Name, entry.Command, colors[i%len(colors)], entry.Port, mgr.output))
			}
		}
		if l == len(mgr.procs) {
			fmt.Printf("No process named %s in Procfile.dev\n", name)
			os.Exit(1)
		}
	}

	mgr.Run()
}

func (mgr *manager) runProcess(proc *process) {
	mgr.procWg.Add(1)

	go func() {
		defer mgr.procWg.Done()
		defer func() { mgr.done <- true }()

		proc.Run()
	}()
}

func (mgr *manager) waitForExit() {
	select {
	case <-mgr.done:
	case <-mgr.interrupted:
	}

	for _, proc := range mgr.procs {
		go proc.Interrupt()
	}

	select {
	case <-time.After(mgr.timeout):
	case <-mgr.interrupted:
	}

	for _, proc := range mgr.procs {
		go proc.Kill()
	}
}

func (mgr *manager) Run() {
	fmt.Printf("\033]0;%s | procman\007", mgr.title)

	mgr.done = make(chan bool, len(mgr.procs))

	mgr.interrupted = make(chan os.Signal)
	signal.Notify(mgr.interrupted, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	for _, proc := range mgr.procs {
		mgr.runProcess(proc)
	}

	go mgr.waitForExit()

	mgr.procWg.Wait()
}

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

func (out *output) Connect(proc *process) {
	if len(proc.Name) > out.maxNameLength {
		out.maxNameLength = len(proc.Name)
	}

	if out.pipes == nil {
		out.pipes = make(map[*process]*ptyPipe)
	}

	out.pipes[proc] = &ptyPipe{}
}

func (out *output) PipeOutput(proc *process) {
	pipe := out.openPipe(proc)

	go func(proc *process, pipe *ptyPipe) {
		scanLines(pipe.pty, func(b []byte) bool {
			out.WriteLine(proc, b)
			return true
		})
	}(proc, pipe)
}

func (out *output) ClosePipe(proc *process) {
	if pipe := out.pipes[proc]; pipe != nil {
		pipe.pty.Close()
		pipe.tty.Close()
	}
}

func (out *output) WriteLine(proc *process, p []byte) {
	var buf bytes.Buffer
	color := fmt.Sprintf("\033[1;38;5;%vm", proc.Color)

	buf.WriteString(color)
	buf.WriteString(proc.Name)

	for i := len(proc.Name); i <= out.maxNameLength; i++ {
		buf.WriteByte(' ')
	}

	buf.WriteString("\033[0m| ")
	buf.Write(p)
	buf.WriteByte('\n')

	out.mutex.Lock()
	defer out.mutex.Unlock()

	buf.WriteTo(os.Stdout)
}

func (out *output) WriteErr(proc *process, err error) {
	out.WriteLine(proc, []byte(
		fmt.Sprintf("\033[0;31m%v\033[0m", err),
	))
}

func newProcess(name, command string, color int, port int, output *output) (proc *process) {
	proc = &process{
		exec.Command("/bin/sh", "-c", command),
		name,
		color,
		output,
	}

	proc.Dir = "./"
	proc.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", port))

	proc.output.Connect(proc)

	return
}

func (proc *process) Running() bool {
	return proc.Process != nil && proc.ProcessState == nil
}

func (proc *process) Run() {
	proc.output.PipeOutput(proc)
	defer proc.output.ClosePipe(proc)

	if err := proc.Cmd.Run(); err != nil {
		proc.output.WriteErr(proc, err)
	}
}

func (proc *process) Interrupt() {
	if proc.Running() {
		proc.signal(syscall.SIGINT)
	}
}

func (proc *process) Kill() {
	if proc.Running() {
		proc.signal(syscall.SIGKILL)
	}
}

func (proc *process) signal(sig os.Signal) {
	group, err := os.FindProcess(-proc.Process.Pid)
	if err != nil {
		proc.output.WriteErr(proc, err)
		return
	}

	if err = group.Signal(sig); err != nil {
		proc.output.WriteErr(proc, err)
	}
}

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
