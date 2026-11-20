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
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/creack/pty"
	"github.com/fsnotify/fsnotify"
)

const (
	timeout        = 5 * time.Second
	debounce       = 100 * time.Millisecond
	maxIgnoreBytes = 1 << 20
	maxIgnoreRules = 10000
	maxIgnoreLine  = 4096
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
	output       *output
	procs        []*process
	mu           sync.Mutex
	active       int
	allDone      chan struct{}
	shuttingDown bool
	done         chan struct{}
	interrupted  chan os.Signal

	// shared file watching
	fsw         *fsnotify.Watcher
	watched     []*process // the procs with watch patterns
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

	restartMu sync.Mutex // serializes restarts for this process
	mu        sync.Mutex // guards cmd and cmdDone
	cmd       *exec.Cmd
	cmdDone   chan struct{} // closed when cmd.Wait() returns
}

// output manages the output display of processes
type output struct {
	maxNameLength int
	mutex         sync.Mutex
	pipes         map[*process]*os.File
	buf           []byte // line buffer for writeLine; mutex guards it
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

	go mgr.waitForExit()

	// Hold one active slot until the start loop finishes so an early
	// exit does not close allDone before later processes start.
	mgr.addActive()
	startFailed := false
	for _, proc := range mgr.procs {
		if err := mgr.runProcess(proc); err != nil {
			proc.output.writeErr(proc, fmt.Errorf("start: %v", err))
			startFailed = true
			mgr.triggerDone()
			break
		}
	}
	mgr.releaseActive()

	if !startFailed {
		mgr.setupWatchers()
	}

	mgr.wait()
	if startFailed {
		os.Exit(1)
	}
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

// addActive increments the count of running processes and restarts.
func (mgr *manager) addActive() {
	mgr.mu.Lock()
	if mgr.allDone == nil {
		mgr.allDone = make(chan struct{})
	}
	mgr.active++
	mgr.mu.Unlock()
}

// releaseActive decrements the active count and closes allDone at zero.
func (mgr *manager) releaseActive() {
	mgr.mu.Lock()
	mgr.active--
	if mgr.active == 0 && mgr.allDone != nil {
		close(mgr.allDone)
	}
	mgr.mu.Unlock()
}

// wait blocks until all active processes and restarts finish.
func (mgr *manager) wait() {
	mgr.mu.Lock()
	if mgr.active == 0 {
		mgr.mu.Unlock()
		return
	}
	ch := mgr.allDone
	mgr.mu.Unlock()
	<-ch
}

// triggerDone signals waitForExit without blocking.
func (mgr *manager) triggerDone() {
	select {
	case mgr.done <- struct{}{}:
	default:
	}
}

// isShuttingDown reports whether shutdown has started.
func (mgr *manager) isShuttingDown() bool {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	return mgr.shuttingDown
}

// runProcess starts proc under a pty and waits for it in a goroutine.
func (mgr *manager) runProcess(proc *process) error {
	if mgr.isShuttingDown() {
		return nil
	}

	cmd := exec.Command("/bin/sh", "-c", proc.cmdStr)
	ptyFile, err := pty.Start(cmd)
	if err != nil {
		return err
	}

	cmdDone := make(chan struct{})
	mgr.mu.Lock()
	if mgr.shuttingDown {
		mgr.mu.Unlock()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		_ = ptyFile.Close()
		return nil
	}
	if mgr.allDone == nil {
		mgr.allDone = make(chan struct{})
	}
	mgr.active++
	proc.mu.Lock()
	proc.cmd = cmd
	proc.cmdDone = cmdDone
	proc.mu.Unlock()
	mgr.mu.Unlock()

	proc.output.pipeOutput(proc, ptyFile)

	go func() {
		_ = cmd.Wait()
		proc.mu.Lock()
		if proc.cmd == cmd {
			proc.cmd = nil
		}
		proc.mu.Unlock()
		proc.output.closePipe(proc, ptyFile)
		close(cmdDone)
		// Only signal done for unwatched processes.
		if len(proc.watchPatterns) == 0 {
			mgr.triggerDone()
		}
		mgr.releaseActive()
	}()
	return nil
}

// waitForExit waits for a process exit or signal, then stops all processes.
func (mgr *manager) waitForExit() {
	defer signal.Stop(mgr.interrupted)

	select {
	case <-mgr.done:
	case <-mgr.interrupted:
	}

	mgr.mu.Lock()
	mgr.shuttingDown = true
	mgr.mu.Unlock()

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

// interrupt sends SIGINT to a running process group.
func (proc *process) interrupt() {
	proc.signal(syscall.SIGINT)
}

// kill sends SIGKILL to a running process group.
func (proc *process) kill() {
	proc.signal(syscall.SIGKILL)
}

// signal sends sig to the process group if the child is still running.
func (proc *process) signal(sig syscall.Signal) {
	proc.mu.Lock()
	var pid int
	if proc.cmd != nil && proc.cmd.Process != nil {
		pid = proc.cmd.Process.Pid
	}
	if pid == 0 {
		proc.mu.Unlock()
		return
	}
	err := syscall.Kill(-pid, sig)
	proc.mu.Unlock()
	if err != nil && !errors.Is(err, syscall.ESRCH) {
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

// pipeOutput reads lines from ptyFile and writes them with a prefix.
func (out *output) pipeOutput(proc *process, ptyFile *os.File) {
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

// closePipe closes ptyFile and removes it from out.pipes.
func (out *output) closePipe(proc *process, ptyFile *os.File) {
	out.mutex.Lock()
	if out.pipes[proc] == ptyFile {
		delete(out.pipes, proc)
	}
	out.mutex.Unlock()

	if ptyFile != nil {
		ptyFile.Close()
	}
}

// writeLine writes a line of output for the specified process, with color formatting.
//
// Every line of process output comes here. So it builds the line in
// out.buf, which it keeps from line to line, and allocates nothing
// after the first long line.
func (out *output) writeLine(proc *process, p []byte) {
	out.mutex.Lock()
	defer out.mutex.Unlock()

	buf := append(out.buf[:0], "\033[1;38;5;"...)
	buf = strconv.AppendInt(buf, int64(proc.color), 10)
	buf = append(buf, 'm')
	buf = append(buf, proc.name...)
	for i := len(proc.name); i <= out.maxNameLength; i++ {
		buf = append(buf, ' ')
	}
	buf = append(buf, "\033[0m| "...)
	buf = append(buf, p...)
	buf = append(buf, '\n')
	out.buf = buf
	os.Stdout.Write(buf)
}

// writeErr writes an error message for the specified process.
func (out *output) writeErr(proc *process, err error) {
	out.writeLine(proc, fmt.Appendf(nil, "\033[0;31m%v\033[0m", err))
}

// setupWatchers creates a single watcher for all processes with watch patterns.
func (mgr *manager) setupWatchers() {
	for _, p := range mgr.procs {
		if len(p.watchPatterns) > 0 {
			mgr.watched = append(mgr.watched, p)
		}
	}
	if len(mgr.watched) == 0 {
		return
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		mgr.watchErr(fmt.Errorf("watch setup: %v", err))
		return
	}
	mgr.fsw = fsw
	mgr.watchDir, _ = os.Getwd()
	mgr.watchStopCh = make(chan struct{})
	mgr.watchTimers = make(map[*process]*time.Timer)

	start := time.Now()
	ignore, err := loadIgnoreRules(".gitignore")
	if err != nil {
		mgr.watchErr(err)
	}
	dirs := watchDirs(mgr.watched, ignore)
	if mgr.isShuttingDown() {
		mgr.fsw.Close()
		mgr.fsw = nil
		return
	}
	count := 0
	for d := range dirs {
		if err := mgr.fsw.Add(d); err != nil {
			mgr.watchErr(fmt.Errorf("watch %s: %v", d, err))
			continue
		}
		count++
	}
	if count == 0 && len(dirs) > 0 {
		mgr.fsw.Close()
		mgr.fsw = nil
		return
	}
	first := mgr.procs[0]
	first.output.writeLine(first, fmt.Appendf(nil,
		"\033[0;90mwatching %d dirs (setup %v)\033[0m", count, time.Since(start).Round(time.Millisecond)))
	go mgr.watchLoop()
}

// watchErr writes a watcher error under the first process.
// setupWatchers returns before any call when there is no process.
func (mgr *manager) watchErr(err error) {
	first := mgr.procs[0]
	first.output.writeErr(first, err)
}

// watchDirs returns the directories under the working directory that
// hold a file a watch pattern of procs can match.
//
// The walk from a base directory gives the same directories for every
// pattern with that base. So watchDirs walks each base one time.
func watchDirs(procs []*process, ignore *ignoreRules) map[string]bool {
	rootReal, _ := filepath.EvalSymlinks(".")
	dirs := make(map[string]bool)
	walked := make(map[string]bool)
	for _, proc := range procs {
		for _, pattern := range proc.watchPatterns {
			matches, err := doublestar.Glob(os.DirFS("."), pattern, doublestar.WithNoFollow())
			if err != nil {
				proc.output.writeErr(proc, fmt.Errorf("glob %q: %v", pattern, err))
				continue
			}
			for _, m := range matches {
				dir := filepath.Dir(m)
				if !dirs[dir] && !ignore.skip(dir) && insideRoot(rootReal, dir) {
					dirs[dir] = true
				}
			}
			base := nonGlobBaseDir(pattern)
			if walked[base] {
				continue
			}
			walked[base] = true
			if !insideRoot(rootReal, base) {
				continue
			}
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
	return dirs
}

// insideRoot reports whether dir resolves to rootReal or a subdirectory of it.
func insideRoot(rootReal, dir string) bool {
	if rootReal == "" {
		return true
	}
	realPath, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootReal, realPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
func loadIgnoreRules(path string) (*ignoreRules, error) {
	ig := &ignoreRules{}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ig, nil
		}
		return ig, fmt.Errorf("ignore %s: %v", path, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return ig, fmt.Errorf("ignore %s: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		return ig, fmt.Errorf("ignore %s: not a regular file", path)
	}

	lr := &io.LimitedReader{R: file, N: maxIgnoreBytes + 1}
	scanner := bufio.NewScanner(lr)
	var limitErr error
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) > maxIgnoreLine {
			limitErr = fmt.Errorf("ignore %s: line exceeds %d bytes", path, maxIgnoreLine)
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
		r.pattern = escapeBraces(strings.TrimSuffix(line, "/"))
		if r.pattern == "" {
			continue
		}
		if len(ig.rules) >= maxIgnoreRules {
			return ig, fmt.Errorf("ignore %s: exceeds %d rules", path, maxIgnoreRules)
		}
		ig.rules = append(ig.rules, r)
	}
	if err := scanner.Err(); err != nil {
		return ig, fmt.Errorf("ignore %s: %v", path, err)
	}
	if lr.N == 0 {
		return ig, fmt.Errorf("ignore %s: exceeds %d bytes", path, maxIgnoreBytes)
	}
	return ig, limitErr
}

// escapeBraces escapes { and } so doublestar treats gitignore braces as literal characters.
func escapeBraces(s string) string {
	if !strings.ContainsAny(s, "{}") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			b.WriteByte(s[i])
			i++
			b.WriteByte(s[i])
			continue
		}
		if s[i] == '{' || s[i] == '}' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
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

// relPath gives the path of a file event relative to the watch
// directory, in the form that the watch patterns match.
func (mgr *manager) relPath(path string) string {
	if mgr.watchDir != "" {
		if rel, err := filepath.Rel(mgr.watchDir, path); err == nil {
			path = rel
		}
	}
	// A watch on "." names its files "./x" under inotify and "x" under
	// kqueue, and "*.rb" matches only the second.
	return filepath.Clean(path)
}

// matchPatterns reports whether a pattern matches rel, a path from
// relPath.
func (mgr *manager) matchPatterns(patterns []string, rel string) bool {
	for _, pattern := range patterns {
		matched, err := doublestar.Match(pattern, rel)
		if err != nil {
			mgr.watchErr(fmt.Errorf("pattern %q: %v", pattern, err))
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
			rel := mgr.relPath(event.Name)
			for _, proc := range mgr.watched {
				if mgr.matchPatterns(proc.watchPatterns, rel) {
					mgr.scheduleRestart(proc)
				}
			}
		case err, ok := <-mgr.fsw.Errors:
			if !ok {
				return
			}
			mgr.watchErr(err)
		}
	}
}

func (mgr *manager) scheduleRestart(proc *process) {
	if mgr.isShuttingDown() {
		return
	}
	mgr.watchMu.Lock()
	defer mgr.watchMu.Unlock()

	if t := mgr.watchTimers[proc]; t != nil {
		t.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(debounce, func() {
		mgr.restart(proc)
		mgr.watchMu.Lock()
		if mgr.watchTimers[proc] == timer {
			delete(mgr.watchTimers, proc)
		}
		mgr.watchMu.Unlock()
	})
	mgr.watchTimers[proc] = timer
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
	p.restartMu.Lock()
	defer p.restartMu.Unlock()

	mgr.mu.Lock()
	if mgr.shuttingDown || mgr.active == 0 {
		mgr.mu.Unlock()
		return
	}
	mgr.active++
	mgr.mu.Unlock()
	defer mgr.releaseActive()

	p.output.writeLine(p, []byte("\033[0;33mrestarting...\033[0m"))

	p.mu.Lock()
	cmdDone := p.cmdDone
	p.mu.Unlock()

	p.interrupt()

	// Wait for the old process to exit.
	if cmdDone != nil {
		select {
		case <-cmdDone:
		case <-time.After(timeout):
			p.kill()
			<-cmdDone
		}
	}

	if err := mgr.runProcess(p); err != nil {
		p.output.writeErr(p, fmt.Errorf("restart: %v", err))
	}
}
