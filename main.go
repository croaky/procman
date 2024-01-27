package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/pkg/term/termios"
	"github.com/urfave/cli"
)

type hivemindConfig struct {
	Title              string
	Procfile           string
	ProcNames          string
	Root               string
	PortBase, PortStep int
	Timeout            int
	NoPrefix           bool
	PrintTimestamps    bool
}

type hivemind struct {
	title       string
	output      *multiOutput
	procs       []*process
	procWg      sync.WaitGroup
	done        chan bool
	interrupted chan os.Signal
	timeout     time.Duration
}

type ptyPipe struct {
	pty, tty *os.File
}

type multiOutput struct {
	maxNameLength  int
	mutex          sync.Mutex
	pipes          map[*process]*ptyPipe
	printProcName  bool
	printTimestamp bool
}

type process struct {
	*exec.Cmd

	Name  string
	Color int

	output *multiOutput
}

type procfileEntry struct {
	Name    string
	Command string
	Port    int
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	var (
		conf hivemindConfig
	)

	app := cli.NewApp()

	app.ArgsUsage = "[procfile] (Use '-' to read from stdin, Procfile path can be also set with $HIVEMIND_PROCFILE)"

	app.Flags = []cli.Flag{
		cli.StringFlag{Name: "title, w", EnvVar: "HIVEMIND_TITLE", Usage: "Specify a title of the application", Destination: &conf.Title},
		cli.StringFlag{Name: "processes, l", EnvVar: "HIVEMIND_PROCESSES", Usage: "Specify process names to launch. Divide names with comma", Destination: &conf.ProcNames},
		cli.IntFlag{Name: "port, p", EnvVar: "HIVEMIND_PORT,PORT", Usage: "specify a port to use as the base", Value: 5000, Destination: &conf.PortBase},
		cli.IntFlag{Name: "port-step, P", EnvVar: "HIVEMIND_PORT_STEP", Usage: "specify a step to increase port number", Value: 100, Destination: &conf.PortStep},
		cli.StringFlag{Name: "root, d", EnvVar: "HIVEMIND_ROOT", Usage: "specify a working directory of application. Default: directory containing the Procfile", Destination: &conf.Root},
		cli.IntFlag{Name: "timeout, t", EnvVar: "HIVEMIND_TIMEOUT", Usage: "specify the amount of time (in seconds) processes have to shut down gracefully before being brutally killed", Value: 5, Destination: &conf.Timeout},
		cli.BoolFlag{Name: "no-prefix", EnvVar: "HIVEMIND_NO_PREFIX", Usage: "process names will not be printed if the flag is specified", Destination: &conf.NoPrefix},
		cli.BoolFlag{Name: "print-timestamps, T", EnvVar: "HIVEMIND_PRINT_TIMESTAMPS", Usage: "timestamps will be printed if the flag is specified", Destination: &conf.PrintTimestamps},
	}

	app.Action = func(c *cli.Context) error {
		switch c.NArg() {
		case 0:
			if path := os.Getenv("HIVEMIND_PROCFILE"); len(path) > 0 {
				conf.Procfile = path
			} else {
				conf.Procfile = "./Procfile"
			}
		case 1:
			conf.Procfile = c.Args().First()
		default:
			fatal("Specify a single procfile")
		}

		if conf.Timeout < 1 {
			fatal("Timeout should be greater than 0")
		}

		if len(conf.Root) == 0 {
			conf.Root = filepath.Dir(conf.Procfile)
		}

		conf.Root, err = filepath.Abs(conf.Root)
		fatalOnErr(err)

		newHivemind(conf).Run()

		return nil
	}

	app.Run(os.Args)
}

func ensureKill(p *process) {
	// p.SysProcAttr.Pdeathsig in supported on on Linux, we can't do anything here
}

var colors = []int{2, 3, 4, 5, 6, 42, 130, 103, 129, 108}

func newHivemind(conf hivemindConfig) (h *hivemind) {
	h = &hivemind{timeout: time.Duration(conf.Timeout) * time.Second}

	if len(conf.Title) > 0 {
		h.title = conf.Title
	} else {
		h.title = filepath.Base(conf.Root)
	}

	h.output = &multiOutput{printProcName: !conf.NoPrefix, printTimestamp: conf.PrintTimestamps}

	entries := parseProcfile(conf.Procfile, conf.PortBase, conf.PortStep)
	h.procs = make([]*process, 0)

	procNames := splitAndTrim(conf.ProcNames)

	for i, entry := range entries {
		if len(procNames) == 0 || stringsContain(procNames, entry.Name) {
			h.procs = append(h.procs, newProcess(entry.Name, entry.Command, colors[i%len(colors)], conf.Root, entry.Port, h.output))
		}
	}

	return
}

func (h *hivemind) runProcess(proc *process) {
	h.procWg.Add(1)

	go func() {
		defer h.procWg.Done()
		defer func() { h.done <- true }()

		proc.Run()
	}()
}

func (h *hivemind) waitForDoneOrInterrupt() {
	select {
	case <-h.done:
	case <-h.interrupted:
	}
}

func (h *hivemind) waitForTimeoutOrInterrupt() {
	select {
	case <-time.After(h.timeout):
	case <-h.interrupted:
	}
}

func (h *hivemind) waitForExit() {
	h.waitForDoneOrInterrupt()

	for _, proc := range h.procs {
		go proc.Interrupt()
	}

	h.waitForTimeoutOrInterrupt()

	for _, proc := range h.procs {
		go proc.Kill()
	}
}

func (h *hivemind) Run() {
	fmt.Printf("\033]0;%s | hivemind\007", h.title)

	h.done = make(chan bool, len(h.procs))

	h.interrupted = make(chan os.Signal)
	signal.Notify(h.interrupted, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	for _, proc := range h.procs {
		h.runProcess(proc)
	}

	go h.waitForExit()

	h.procWg.Wait()
}

func (m *multiOutput) openPipe(proc *process) (pipe *ptyPipe) {
	var err error

	pipe = m.pipes[proc]

	pipe.pty, pipe.tty, err = termios.Pty()
	fatalOnErr(err)

	proc.Stdout = pipe.tty
	proc.Stderr = pipe.tty
	proc.Stdin = pipe.tty
	proc.SysProcAttr = &syscall.SysProcAttr{Setctty: true, Setsid: true}

	return
}

func (m *multiOutput) Connect(proc *process) {
	if len(proc.Name) > m.maxNameLength {
		m.maxNameLength = len(proc.Name)
	}

	if m.pipes == nil {
		m.pipes = make(map[*process]*ptyPipe)
	}

	m.pipes[proc] = &ptyPipe{}
}

func (m *multiOutput) PipeOutput(proc *process) {
	pipe := m.openPipe(proc)

	go func(proc *process, pipe *ptyPipe) {
		scanLines(pipe.pty, func(b []byte) bool {
			m.WriteLine(proc, b)
			return true
		})
	}(proc, pipe)
}

func (m *multiOutput) ClosePipe(proc *process) {
	if pipe := m.pipes[proc]; pipe != nil {
		pipe.pty.Close()
		pipe.tty.Close()
	}
}

func (m *multiOutput) WriteLine(proc *process, p []byte) {
	var buf bytes.Buffer

	if m.printProcName || m.printTimestamp {
		color := fmt.Sprintf("\033[1;38;5;%vm", proc.Color)

		buf.WriteString(color)

		if m.printTimestamp {
			buf.WriteString(time.Now().Format("15:04:05"))
			buf.WriteByte(' ')
		}

		if m.printProcName {
			buf.WriteString(proc.Name)

			for i := len(proc.Name); i <= m.maxNameLength; i++ {
				buf.WriteByte(' ')
			}
		}

		buf.WriteString("\033[0m| ")
	}

	buf.Write(p)
	buf.WriteByte('\n')

	m.mutex.Lock()
	defer m.mutex.Unlock()

	buf.WriteTo(os.Stdout)
}

func (m *multiOutput) WriteErr(proc *process, err error) {
	m.WriteLine(proc, []byte(
		fmt.Sprintf("\033[0;31m%v\033[0m", err),
	))
}

func newProcess(name, command string, color int, root string, port int, output *multiOutput) (proc *process) {
	proc = &process{
		exec.Command("/bin/sh", "-c", command),
		name,
		color,
		output,
	}

	proc.Dir = root
	proc.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", port))

	proc.output.Connect(proc)

	return
}

func (p *process) writeLine(b []byte) {
	p.output.WriteLine(p, b)
}

func (p *process) writeErr(err error) {
	p.output.WriteErr(p, err)
}

func (p *process) signal(sig os.Signal) {
	group, err := os.FindProcess(-p.Process.Pid)
	if err != nil {
		p.writeErr(err)
		return
	}

	if err = group.Signal(sig); err != nil {
		p.writeErr(err)
	}
}

func (p *process) Running() bool {
	return p.Process != nil && p.ProcessState == nil
}

func (p *process) Run() {
	p.output.PipeOutput(p)
	defer p.output.ClosePipe(p)

	ensureKill(p)

	p.writeLine([]byte("\033[1mRunning...\033[0m"))

	if err := p.Cmd.Run(); err != nil {
		p.writeErr(err)
	} else {
		p.writeLine([]byte("\033[1mProcess exited\033[0m"))
	}
}

func (p *process) Interrupt() {
	if p.Running() {
		p.writeLine([]byte("\033[1mInterrupting...\033[0m"))
		p.signal(syscall.SIGINT)
	}
}

func (p *process) Kill() {
	if p.Running() {
		p.writeLine([]byte("\033[1mKilling...\033[0m"))
		p.signal(syscall.SIGKILL)
	}
}

func parseProcfile(path string, portBase, portStep int) (entries []procfileEntry) {
	var f io.Reader
	switch path {
	case "-":
		f = os.Stdin
	default:
		file, err := os.Open(path)
		fatalOnErr(err)
		defer file.Close()

		f = file
	}

	re, _ := regexp.Compile(`^([\w-]+):\s+(.+)$`)
	port := portBase
	names := make(map[string]bool)

	err := scanLines(f, func(b []byte) bool {
		if len(b) == 0 {
			return true
		}

		params := re.FindStringSubmatch(string(b))
		if len(params) != 3 {
			return true
		}

		name, cmd := params[1], params[2]

		if names[name] {
			fatal("Process names must be uniq")
		}
		names[name] = true

		entries = append(entries, procfileEntry{name, cmd, port})

		port += portStep

		return true
	})

	fatalOnErr(err)

	if len(entries) == 0 {
		fatal("No entries was found in Procfile")
	}

	return
}

func fatalOnErr(err error) {
	if err != nil {
		fatal(err)
	}
}

func fatal(i ...interface{}) {
	fmt.Fprint(os.Stderr, "hivemind: ")
	fmt.Fprintln(os.Stderr, i...)
	os.Exit(1)
}

func splitAndTrim(str string) (res []string) {
	split := strings.Split(str, ",")
	for _, s := range split {
		s = strings.Trim(s, " ")
		if len(s) > 0 {
			res = append(res, s)
		}
	}
	return
}

func stringsContain(strs []string, str string) bool {
	for _, s := range strs {
		if s == str {
			return true
		}
	}
	return false
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
