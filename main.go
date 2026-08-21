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
	debounce = 100 * time.Millisecond
)

var (
	colors     = []int{2, 3, 4, 5, 6, 42, 130, 103, 129, 108}
	procfileRe = regexp.MustCompile(`^([\w-]+):\s+(.+?)(?:\s+#\s*(.+))?$`)
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
	procWg      sync.WaitGroup
	done        chan struct{}
	interrupted chan os.Signal

	// shared file watching
	fsw         *fsnotify.Watcher
	watchDir    string
	watchStopCh chan struct{}
	watchOnce   sync.Once
	watchMu     sync.Mutex
	watchTimers map[*process]*time.Timer
}

// process represents an individual process to be managed
type process struct {
	name          string
	cmdStr        string
	color         int
	output        *output
	watchPatterns []string

	mu      sync.Mutex
	cmd     *exec.Cmd
	cmdDone chan struct{} // closed when cmd.Wait() returns
}

// output manages the output display of processes
type output struct {
	maxNameLength int
	mutex         sync.Mutex
	pipes         map[*process]*os.File
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
	for s := range strings.SplitSeq(args[1], ",") {
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
			patterns = parseWatchPatterns(params[3])
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

// parseWatchPatterns extracts watch patterns from a comment string.
// Example: "watch: lib/**/*.rb,ui/**/*.haml"
func parseWatchPatterns(comment string) []string {
	_, after, found := strings.Cut(comment, "watch:")
	if !found {
		return nil
	}
	watchStr := strings.TrimSpace(after)
	var patterns []string
	for p := range strings.SplitSeq(watchStr, ",") {
		if p = strings.TrimSpace(p); p != "" {
			patterns = append(patterns, p)
		}
	}
	return patterns
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
			name:          name,
			cmdStr:        def.cmd,
			color:         colors[i%len(colors)],
			output:        mgr.output,
			watchPatterns: def.watchPatterns,
		}
		mgr.procs = append(mgr.procs, proc)
	}
	return nil
}

// setupSignalHandling configures handling of interrupt signals.
func (mgr *manager) setupSignalHandling() {
	mgr.done = make(chan struct{}, len(mgr.procs))
	mgr.interrupted = make(chan os.Signal, 1)
	signal.Notify(mgr.interrupted, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
}

// runProcess creates a new command, starts it, and waits for it in a goroutine.
func (mgr *manager) runProcess(proc *process) {
	proc.cmd = exec.Command("/bin/sh", "-c", proc.cmdStr)
	proc.cmdDone = make(chan struct{})

	proc.output.pipeOutput(proc)

	mgr.procWg.Go(func() {
		proc.cmd.Wait()
		proc.output.closePipe(proc)
		close(proc.cmdDone)
		// Only signal done for unwatched processes
		if len(proc.watchPatterns) == 0 {
			mgr.done <- struct{}{}
		}
	})
}

// waitForExit waits for all processes to exit or for an interruption signal.
func (mgr *manager) waitForExit() {
	select {
	case <-mgr.done:
	case <-mgr.interrupted:
	}

	mgr.stopWatcher()

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

// interrupt sends an interrupt signal to a running process.
func (proc *process) interrupt() {
	if proc.cmd != nil && proc.cmd.Process != nil {
		proc.signal(syscall.SIGINT)
	}
}

// kill forcefully stops a running process.
func (proc *process) kill() {
	if proc.cmd != nil && proc.cmd.Process != nil {
		proc.signal(syscall.SIGKILL)
	}
}

// signal sends a specified signal to the process group.
func (proc *process) signal(sig syscall.Signal) {
	if err := syscall.Kill(-proc.cmd.Process.Pid, sig); err != nil {
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
	ptyFile, err := pty.Start(proc.cmd)
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
		// EIO is how the master side of a pty reports that the child
		// closed the slave, which is every process exiting normally.
		if err := scanner.Err(); err != nil &&
			!errors.Is(err, os.ErrClosed) && !errors.Is(err, syscall.EIO) {
			out.writeErr(proc, err)
		}
	}()
}

// closePipe closes the pseudo-terminal associated with the process.
func (out *output) closePipe(proc *process) {
	out.mutex.Lock()
	ptyFile := out.pipes[proc]
	delete(out.pipes, proc)
	out.mutex.Unlock()

	if ptyFile != nil {
		ptyFile.Close()
	}
}

// writeLine writes a line of output for the specified process, with color formatting.
func (out *output) writeLine(proc *process, p []byte) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "\033[1;38;5;%vm", proc.color)
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
	out.writeLine(proc, fmt.Appendf(nil, "\033[0;31m%v\033[0m", err))
}

// setupWatchers creates a single watcher for all processes with watch patterns.
func (mgr *manager) setupWatchers() {
	has := false
	for _, p := range mgr.procs {
		if len(p.watchPatterns) > 0 {
			has = true
			break
		}
	}
	if !has {
		return
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		if len(mgr.procs) > 0 {
			mgr.procs[0].output.writeErr(mgr.procs[0], fmt.Errorf("watch setup: %v", err))
		}
		return
	}
	mgr.fsw = fsw
	mgr.watchDir, _ = os.Getwd()
	mgr.watchStopCh = make(chan struct{})
	mgr.watchTimers = make(map[*process]*time.Timer)

	start := time.Now()
	ignore := loadIgnoreRules(".gitignore")
	dirs := make(map[string]bool)
	for _, proc := range mgr.procs {
		for _, pattern := range proc.watchPatterns {
			matches, err := doublestar.Glob(os.DirFS("."), pattern)
			if err != nil {
				proc.output.writeErr(proc, fmt.Errorf("glob %q: %v", pattern, err))
				continue
			}
			for _, m := range matches {
				if dir := filepath.Dir(m); !ignore.skip(dir) {
					dirs[dir] = true
				}
			}
			base := nonGlobBaseDir(pattern)
			_ = filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if d.IsDir() {
					if ignore.skip(path) {
						return filepath.SkipDir
					}
					dirs[path] = true
				}
				return nil
			})
		}
	}
	count := 0
	for d := range dirs {
		if err := mgr.fsw.Add(d); err != nil {
			if len(mgr.procs) > 0 {
				mgr.procs[0].output.writeErr(mgr.procs[0], fmt.Errorf("watch %s: %v", d, err))
			}
			continue
		}
		count++
	}
	if count == 0 && len(dirs) > 0 {
		mgr.fsw.Close()
		mgr.fsw = nil
		return
	}
	if len(mgr.procs) > 0 {
		mgr.procs[0].output.writeLine(mgr.procs[0], fmt.Appendf(nil,
			"\033[0;90mwatching %d dirs (setup %v)\033[0m", count, time.Since(start).Round(time.Millisecond)))
	}
	go mgr.watchLoop()
}

// alwaysSkip are directories pruned whatever .gitignore says, since
// neither holds source and a watch on either buys nothing.
//
// node_modules is here because .gitignore is the wrong thing to read it
// out of: a project that has stopped building with npm drops the entry
// and leaves the tree on disk, which is when the walk finds it. One
// left over that way cost a watch a hundred megabytes of descriptors
// and a spurious error, since kqueue opens every file in a directory it
// watches and a stale .bin symlink pointed at nothing.
var alwaysSkip = map[string]bool{
	".git":         true,
	"node_modules": true,
}

// ignoreRules holds ordered .gitignore-style rules. Later rules win,
// so a negation (!pattern) can re-include a previously ignored path.
// It always skips alwaysSkip.
type ignoreRules struct {
	rules []ignoreRule
}

type ignoreRule struct {
	pattern  string // anchored, leading slash stripped
	anchored bool   // pattern was rooted with a leading slash
	negate   bool
}

// loadIgnoreRules reads a .gitignore file. A missing file yields rules
// that still skip alwaysSkip, so the watcher stays cheap in a directory
// under no version control at all.
func loadIgnoreRules(path string) *ignoreRules {
	ig := &ignoreRules{}
	file, err := os.Open(path)
	if err != nil {
		return ig
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r := ignoreRule{}
		if strings.HasPrefix(line, "!") {
			r.negate = true
			line = line[1:]
		}
		if strings.HasPrefix(line, "/") {
			r.anchored = true
			line = line[1:]
		}
		r.pattern = strings.TrimSuffix(line, "/")
		if r.pattern == "" {
			continue
		}
		ig.rules = append(ig.rules, r)
	}
	_ = scanner.Err()
	return ig
}

// skip reports whether a directory path should be pruned from the walk.
// path is relative to the walk root (and thus the repo root).
func (ig *ignoreRules) skip(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	base := filepath.Base(clean)
	if alwaysSkip[base] {
		return true
	}
	ignored := false
	for _, r := range ig.rules {
		var matched bool
		if r.anchored {
			matched, _ = doublestar.Match(r.pattern, clean)
		} else {
			// Unanchored: match any path segment, as git does.
			if m, _ := doublestar.Match(r.pattern, base); m {
				matched = true
			} else {
				matched, _ = doublestar.Match(r.pattern, clean)
			}
		}
		if matched {
			ignored = !r.negate
		}
	}
	return ignored
}

func nonGlobBaseDir(pattern string) string {
	// find first index of any glob metachar
	idx := -1
	for i, ch := range pattern {
		if ch == '*' || ch == '?' || ch == '[' {
			idx = i
			break
		}
	}
	if idx <= 0 {
		return "."
	}
	return filepath.Dir(pattern[:idx])
}

func (mgr *manager) matchPatterns(patterns []string, path string) bool {
	if mgr.watchDir != "" {
		if rel, err := filepath.Rel(mgr.watchDir, path); err == nil {
			path = rel
		}
	}
	// A watch on "." names its files "./x" under inotify and "x" under
	// kqueue, and "*.rb" matches only the second.
	path = filepath.Clean(path)
	for _, pattern := range patterns {
		matched, err := doublestar.Match(pattern, path)
		if err != nil {
			if len(mgr.procs) > 0 {
				mgr.procs[0].output.writeErr(mgr.procs[0], fmt.Errorf("pattern %q: %v", pattern, err))
			}
			continue
		}
		if matched {
			return true
		}
	}
	return false
}

func (mgr *manager) watchLoop() {
	for {
		select {
		case <-mgr.watchStopCh:
			mgr.stopAllTimers()
			if mgr.fsw != nil {
				mgr.fsw.Close()
			}
			return
		case event, ok := <-mgr.fsw.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			for _, proc := range mgr.procs {
				if len(proc.watchPatterns) == 0 {
					continue
				}
				if mgr.matchPatterns(proc.watchPatterns, event.Name) {
					mgr.scheduleRestart(proc)
				}
			}
		case err, ok := <-mgr.fsw.Errors:
			if !ok {
				return
			}
			if len(mgr.procs) > 0 {
				mgr.procs[0].output.writeErr(mgr.procs[0], err)
			}
		}
	}
}

func (mgr *manager) scheduleRestart(proc *process) {
	mgr.watchMu.Lock()
	defer mgr.watchMu.Unlock()

	if t := mgr.watchTimers[proc]; t != nil {
		t.Stop()
	}
	mgr.watchTimers[proc] = time.AfterFunc(debounce, func() {
		mgr.restart(proc)
		mgr.watchMu.Lock()
		delete(mgr.watchTimers, proc)
		mgr.watchMu.Unlock()
	})
}

func (mgr *manager) stopAllTimers() {
	mgr.watchMu.Lock()
	defer mgr.watchMu.Unlock()
	for p, t := range mgr.watchTimers {
		t.Stop()
		delete(mgr.watchTimers, p)
	}
}

func (mgr *manager) stopWatcher() {
	mgr.watchOnce.Do(func() {
		if mgr.watchStopCh != nil {
			close(mgr.watchStopCh)
		}
	})
}

func (mgr *manager) restart(p *process) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.output.writeLine(p, []byte("\033[0;33mrestarting...\033[0m"))

	p.interrupt()

	// Wait for process to exit
	select {
	case <-p.cmdDone:
	case <-time.After(timeout):
		p.kill()
		<-p.cmdDone
	}

	mgr.runProcess(p)
}
