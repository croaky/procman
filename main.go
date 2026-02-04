// procman is a process manager for local development on macOS.
// It reads process definitions from Procfile.dev and runs them concurrently,
// combining their output with colored prefixes.
//
// Usage:
//
//	procman web,worker
package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/creack/pty"
	"github.com/fsnotify/fsnotify"
)

const (
	timeout  = 5 * time.Second
	debounce = 500 * time.Millisecond
)

var (
	colors     = []int{2, 3, 4, 5, 6, 42, 130, 103, 129, 108}
	procfileRe = regexp.MustCompile(`^([\w-]+):\s+(.+?)(?:\s+#\s*watch:\s*(.+))?$`)
)

// procDef represents a single line in the procfile, with a name and command
type procDef struct {
	name          string
	cmd           string
	watchPatterns []string
}

// manager handles overall process management
type manager struct {
	output      *output
	procs       []*process
	watchers    []*watcher
	procWg      sync.WaitGroup
	done        chan bool
	interrupted chan os.Signal
}

// process represents an individual process to be managed
type process struct {
	*exec.Cmd
	name          string
	cmdStr        string
	color         int
	output        *output
	watchPatterns []string
	mu            sync.Mutex
	restarting    bool
	done          chan struct{} // closed when run() completes
}

// output manages the output display of processes
type output struct {
	maxNameLength int
	mutex         sync.Mutex
	pipes         map[*process]*os.File
}

// watcher monitors files and restarts a process on changes
type watcher struct {
	fsw      *fsnotify.Watcher
	proc     *process
	mgr      *manager
	workDir  string
	stopCh   chan struct{}
	stopOnce sync.Once
}

func main() {
	procNames, err := parseArgs(os.Args)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	entries, err := readProcfile("./Procfile.dev")
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	mgr := &manager{output: &output{}}
	if err = mgr.setupProcesses(entries, procNames); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	mgr.output.init(mgr.procs)

	mgr.setupSignalHandling()

	for _, proc := range mgr.procs {
		mgr.runProcess(proc)
	}

	mgr.setupWatchers()

	go mgr.waitForExit()
	mgr.procWg.Wait()
}

// parseArgs parses command-line arguments and returns a list of process names.
func parseArgs(args []string) ([]string, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("no processes given as arguments")
	}
	var procNames []string
	for _, s := range strings.Split(args[1], ",") {
		if s = strings.TrimSpace(s); s != "" {
			procNames = append(procNames, s)
		}
	}
	return procNames, nil
}

// readProcfile opens the procfile and parses it.
func readProcfile(filename string) ([]procDef, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return parseProcfile(file)
}

// parseProcfile reads and parses the procfile, returning a slice of procDefs.
func parseProcfile(r io.Reader) ([]procDef, error) {
	names := make(map[string]bool)
	var defs []procDef

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		params := procfileRe.FindStringSubmatch(line)
		if len(params) < 3 {
			continue
		}

		name, cmd := params[1], params[2]
		if names[name] {
			return nil, fmt.Errorf("duplicate process name %s in Procfile.dev", name)
		}
		names[name] = true

		var patterns []string
		if len(params) > 3 && params[3] != "" {
			for _, p := range strings.Split(params[3], ",") {
				if p = strings.TrimSpace(p); p != "" {
					patterns = append(patterns, p)
				}
			}
		}
		defs = append(defs, procDef{name: name, cmd: cmd, watchPatterns: patterns})
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(defs) == 0 {
		return nil, errors.New("no procDefs found in Procfile.dev")
	}
	return defs, nil
}

// setupProcesses creates and initializes processes based on the given procDefs.
func (mgr *manager) setupProcesses(defs []procDef, procNames []string) error {
	defMap := make(map[string]procDef)
	for _, def := range defs {
		defMap[def.name] = def
	}

	for i, name := range procNames {
		def, ok := defMap[name]
		if !ok {
			return fmt.Errorf("no process named %s in Procfile.dev", name)
		}

		proc := &process{
			Cmd:           exec.Command("/bin/sh", "-c", def.cmd),
			name:          name,
			cmdStr:        def.cmd,
			color:         colors[i%len(colors)],
			output:        mgr.output,
			watchPatterns: def.watchPatterns,
			done:          make(chan struct{}),
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
		defer func() {
			proc.mu.Lock()
			restarting := proc.restarting
			proc.mu.Unlock()
			if !restarting {
				mgr.done <- true
			}
		}()
		proc.run()
	}()
}

// waitForExit waits for all processes to exit or for an interruption signal.
func (mgr *manager) waitForExit() {
	select {
	case <-mgr.done:
	case <-mgr.interrupted:
	}

	for _, w := range mgr.watchers {
		w.stop()
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
	defer close(proc.done)

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

// setupWatchers creates file watchers for processes with watch patterns.
func (mgr *manager) setupWatchers() {
	for _, proc := range mgr.procs {
		if len(proc.watchPatterns) == 0 {
			continue
		}
		w, err := newWatcher(proc, mgr)
		if err != nil {
			proc.output.writeErr(proc, fmt.Errorf("watch setup: %v", err))
			continue
		}
		mgr.watchers = append(mgr.watchers, w)
		go w.run()
	}
}

// newWatcher creates a watcher for the given process.
func newWatcher(proc *process, mgr *manager) (*watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	wd, _ := os.Getwd()
	w := &watcher{
		fsw:     fsw,
		proc:    proc,
		mgr:     mgr,
		workDir: wd,
		stopCh:  make(chan struct{}),
	}

	// Find and watch directories containing files matching patterns
	dirs := make(map[string]bool)
	for _, pattern := range proc.watchPatterns {
		matches, err := doublestar.Glob(os.DirFS("."), pattern)
		if err != nil {
			proc.output.writeErr(proc, fmt.Errorf("glob %q: %v", pattern, err))
			continue
		}
		for _, match := range matches {
			dir := filepath.Dir(match)
			dirs[dir] = true
		}
		// Also watch parent directories for new files
		if idx := strings.Index(pattern, "*"); idx > 0 {
			base := filepath.Dir(pattern[:idx])
			filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if d.IsDir() {
					dirs[path] = true
				}
				return nil
			})
		}
	}

	watchedCount := 0
	for dir := range dirs {
		if err := fsw.Add(dir); err != nil {
			proc.output.writeErr(proc, fmt.Errorf("watch %s: %v", dir, err))
		} else {
			watchedCount++
		}
	}
	if watchedCount == 0 && len(dirs) > 0 {
		fsw.Close()
		return nil, fmt.Errorf("failed to watch any directories")
	}

	return w, nil
}

// run starts the watch loop.
func (w *watcher) run() {
	var timer *time.Timer
	var timerC <-chan time.Time

	for {
		select {
		case <-w.stopCh:
			if timer != nil {
				timer.Stop()
			}
			w.fsw.Close()
			return
		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if !w.matchesPattern(event.Name) {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			// Debounce
			if timer != nil {
				timer.Stop()
			}
			timer = time.NewTimer(debounce)
			timerC = timer.C
		case <-timerC:
			w.restart()
			timerC = nil
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			w.proc.output.writeErr(w.proc, err)
		}
	}
}

// matchesPattern checks if a file path matches any watch pattern.
func (w *watcher) matchesPattern(path string) bool {
	// fsnotify returns absolute paths; convert to relative for pattern matching
	if w.workDir != "" {
		if rel, err := filepath.Rel(w.workDir, path); err == nil {
			path = rel
		}
	}
	for _, pattern := range w.proc.watchPatterns {
		matched, err := doublestar.Match(pattern, path)
		if err != nil {
			w.proc.output.writeErr(w.proc, fmt.Errorf("pattern %q: %v", pattern, err))
			continue
		}
		if matched {
			return true
		}
	}
	return false
}

// restart stops and restarts the watched process.
func (w *watcher) restart() {
	w.proc.mu.Lock()
	if w.proc.restarting || !w.proc.running() {
		w.proc.mu.Unlock()
		return
	}
	w.proc.restarting = true
	done := w.proc.done
	w.proc.mu.Unlock()

	w.proc.output.writeLine(w.proc, []byte("\033[0;33mrestarting...\033[0m"))
	w.proc.interrupt()

	// Wait for process to exit (with timeout)
	select {
	case <-done:
	case <-time.After(timeout):
		w.proc.kill()
		<-done // still wait for run() to complete
	}

	// Create new command and restart
	w.proc.mu.Lock()
	w.proc.Cmd = exec.Command("/bin/sh", "-c", w.proc.cmdStr)
	w.proc.done = make(chan struct{})
	w.proc.restarting = false
	w.proc.mu.Unlock()

	w.mgr.runProcess(w.proc)
}

// stop signals the watcher to stop.
func (w *watcher) stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })
}
