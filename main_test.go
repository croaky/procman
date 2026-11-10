package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/croaky/is"
)

func TestMain(m *testing.M) {
	cmd := exec.Command("go", "build", "-o", "procman", ".")
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "failed to build: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	os.Remove("procman")
	os.Remove("Procfile.dev")
	os.Exit(code)
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    []string
		wantErr bool
	}{
		{
			name:    "No arguments",
			args:    []string{"procman"},
			want:    nil,
			wantErr: true,
		},
		{
			name:    "Single process",
			args:    []string{"procman", "web"},
			want:    []string{"web"},
			wantErr: false,
		},
		{
			name:    "Multiple processes",
			args:    []string{"procman", "web,db,cache"},
			want:    []string{"web", "db", "cache"},
			wantErr: false,
		},
		{
			name:    "Extra whitespace",
			args:    []string{"procman", "web, db , cache "},
			want:    []string{"web", "db", "cache"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.NewRelaxed(t)

			got, err := parseArgs(tt.args)

			if tt.wantErr {
				is.HasErr(err)
			} else {
				is.NoErr(err)
			}
			is.Eq(got, tt.want)
		})
	}
}

func TestParseProcfile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []procDef
		wantErr bool
	}{
		{
			name:    "Valid procfile",
			content: "web: npm start\nworker: npm run worker",
			want: []procDef{
				{name: "web", cmd: "npm start"},
				{name: "worker", cmd: "npm run worker"},
			},
			wantErr: false,
		},
		{
			name:    "Empty procfile",
			content: "",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "Invalid format",
			content: "web npm start",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "Duplicate names",
			content: "web: npm start\nweb: npm run something",
			want:    nil,
			wantErr: true,
		},
		{
			name:    "With watch patterns",
			content: "web: bundle exec ruby cmd/web.rb  # watch: lib/**/*.rb,ui/**/*.haml",
			want: []procDef{
				{name: "web", cmd: "bundle exec ruby cmd/web.rb", watchPatterns: []string{"lib/**/*.rb", "ui/**/*.haml"}},
			},
			wantErr: false,
		},
		{
			name:    "Mixed with and without watch",
			content: "web: ruby web.rb  # watch: *.rb\nesbuild: bun build",
			want: []procDef{
				{name: "web", cmd: "ruby web.rb", watchPatterns: []string{"*.rb"}},
				{name: "esbuild", cmd: "bun build"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.NewRelaxed(t)

			got, err := parseProcfile(strings.NewReader(tt.content))

			if tt.wantErr {
				is.HasErr(err)
			} else {
				is.NoErr(err)
			}
			is.Eq(got, tt.want)
		})
	}
}

func TestSetupProcesses(t *testing.T) {
	tests := []struct {
		name      string
		defs      []procDef
		procNames []string
		wantError bool
	}{
		{
			name: "Valid setup",
			defs: []procDef{
				{name: "web", cmd: "command1"},
				{name: "db", cmd: "command2"},
			},
			procNames: []string{"web", "db"},
			wantError: false,
		},
		{
			name: "Invalid process name",
			defs: []procDef{
				{name: "web", cmd: "command1"},
			},
			procNames: []string{"web", "api"},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)

			mgr := manager{output: &output{}}
			err := mgr.setupProcesses(tt.defs, tt.procNames)

			if tt.wantError {
				is.HasErr(err)
			} else {
				is.NoErr(err)
			}
		})
	}
}

func TestSetupSignalHandling(t *testing.T) {
	is := is.New(t)

	mgr := manager{output: &output{}, procs: make([]*process, 2)}
	mgr.setupSignalHandling()

	is.NotNil(mgr.done)
	is.NotNil(mgr.interrupted)
}

func TestInit(t *testing.T) {
	is := is.New(t)

	out := &output{}
	procs := []*process{{name: "testProcess"}}

	out.init(procs)

	is.Eq(out.maxNameLength, len(procs[0].name))
	is.NotNil(out.pipes)
}

func TestWriteLine(t *testing.T) {
	is := is.New(t)

	out := &output{maxNameLength: 11} // Length of "testProcess"
	proc := &process{name: "testProcess", color: 31}

	// Capture standard output
	originalStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	out.writeLine(proc, []byte("Hello world"))

	w.Close()
	os.Stdout = originalStdout

	var buf bytes.Buffer
	_, err := buf.ReadFrom(r)
	is.NoErr(err)

	is.Eq(buf.String(), "\033[1;38;5;31mtestProcess \033[0m| Hello world\n")
}

func TestWriteErr(t *testing.T) {
	is := is.New(t)

	out := &output{maxNameLength: 11}
	proc := &process{name: "testProcess", color: 31}
	testErr := fmt.Errorf("test error")

	originalStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	out.writeErr(proc, testErr)

	w.Close()
	os.Stdout = originalStdout

	var buf bytes.Buffer
	_, err := buf.ReadFrom(r)
	is.NoErr(err)

	is.Eq(buf.String(), "\033[1;38;5;31mtestProcess \033[0m| \033[0;31mtest error\033[0m\n")
}

func TestMatchesPattern(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		path     string
		want     bool
	}{
		{
			name:     "Simple glob match",
			patterns: []string{"*.rb"},
			path:     "app.rb",
			want:     true,
		},
		{
			name:     "Simple glob no match",
			patterns: []string{"*.rb"},
			path:     "app.go",
			want:     false,
		},
		{
			name:     "Recursive glob match",
			patterns: []string{"lib/**/*.rb"},
			path:     "lib/models/user.rb",
			want:     true,
		},
		{
			name:     "Recursive glob no match wrong dir",
			patterns: []string{"lib/**/*.rb"},
			path:     "app/models/user.rb",
			want:     false,
		},
		{
			name:     "Multiple patterns first matches",
			patterns: []string{"*.rb", "*.haml"},
			path:     "view.haml",
			want:     true,
		},
		{
			name:     "Multiple patterns none match",
			patterns: []string{"*.rb", "*.haml"},
			path:     "style.css",
			want:     false,
		},
		{
			name:     "Directory pattern match",
			patterns: []string{"ui/**/*.haml"},
			path:     "ui/views/index.haml",
			want:     true,
		},
		{
			// A watch on "." names its files this way under inotify.
			name:     "Dot-slash prefix match",
			patterns: []string{"*.rb"},
			path:     "./app.rb",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)

			mgr := &manager{watchDir: ""} // empty means path is already relative
			got := mgr.matchPatterns(tt.patterns, mgr.relPath(tt.path))

			is.Eq(got, tt.want)
		})
	}
}

// watchTree makes a tree under a temporary directory, changes to it,
// and returns its directories in the order it made them.
func watchTree(t testing.TB, width int) []string {
	t.Chdir(t.TempDir())
	dirs := []string{"."}
	for i := range width {
		for j := range width {
			dir := filepath.Join(fmt.Sprintf("d%d", i), fmt.Sprintf("e%d", j))
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "a.go"), nil, 0644); err != nil {
				t.Fatal(err)
			}
			if j == 0 {
				dirs = append(dirs, filepath.Dir(dir))
			}
			dirs = append(dirs, dir)
		}
	}
	return dirs
}

func TestWatchDirs(t *testing.T) {
	is := is.New(t)

	tree := watchTree(t, 2)
	is.NoErr(os.MkdirAll("tmp/cache", 0755))
	is.NoErr(os.WriteFile("tmp/cache/a.go", nil, 0644))

	procs := []*process{
		{name: "web", watchPatterns: []string{"**/*.go", "**/*.hml"}},
		{name: "worker", watchPatterns: []string{"d0/**/*.go"}},
	}
	got := watchDirs(procs, &ignoreRules{rules: []ignoreRule{{pattern: "tmp"}}})

	// The walk prunes tmp, but the glob still finds a match under it.
	want := map[string]bool{"tmp/cache": true}
	for _, d := range tree {
		want[d] = true
	}
	is.Eq(got, want)
}

func BenchmarkWatchDirs(b *testing.B) {
	watchTree(b, 10)
	procs := []*process{
		{name: "web", watchPatterns: []string{"**/*.go", "**/*.hml"}},
		{name: "css", watchPatterns: []string{"**/*.scss"}},
	}
	ignore := &ignoreRules{}
	b.ReportAllocs()
	for b.Loop() {
		watchDirs(procs, ignore)
	}
}

// benchProcfile is a Procfile.dev of the size a Rails app has.
const benchProcfile = `clock: bundle exec ruby cmd/clock.rb  # watch: lib/**/*.rb,cmd/clock.rb
esbuild: bun run buildwatch
queues: bundle exec ruby cmd/queues.rb  # watch: lib/**/*.rb,cmd/queues.rb
web: bundle exec ruby cmd/web.rb  # watch: lib/**/*.rb,ui/**/*.haml,cmd/web.rb

css: bun run tailwind --watch
postgres: postgres -D tmp/db -p 5432
redis: redis-server --port 6379 --save ""
mail: mailpit --smtp 127.0.0.1:1025
worker-high: bundle exec ruby cmd/worker.rb high  # watch: lib/**/*.rb
worker-low: bundle exec ruby cmd/worker.rb low  # watch: lib/**/*.rb
`

func BenchmarkParseProcfile(b *testing.B) {
	b.SetBytes(int64(len(benchProcfile)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := parseProcfile(strings.NewReader(benchProcfile)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkWriteLine measures the path every line of process output
// takes. It sends os.Stdout to the null device for the run.
func BenchmarkWriteLine(b *testing.B) {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer null.Close()
	stdout := os.Stdout
	os.Stdout = null
	defer func() { os.Stdout = stdout }()

	proc := &process{name: "web", color: 42}
	out := &output{}
	out.init([]*process{proc, {name: "worker-high"}})
	line := []byte(`[67296] - Worker 1 (PID: 67331) booted in 0.9s, phase: 0 GET /assets/app.css 200`)
	b.SetBytes(int64(len(line)))
	b.ReportAllocs()
	for b.Loop() {
		out.writeLine(proc, line)
	}
}

func BenchmarkIgnoreRulesSkip(b *testing.B) {
	path := filepath.Join(b.TempDir(), ".gitignore")
	gitignore := "# deps\n/vendor/bundle\nnode_modules/\n\n# build\n/public/assets\n/public/packs\n" +
		"dist/\ncoverage/\n*.log\n/tmp\n!/tmp/.keep\n/log\n.DS_Store\n/storage\n.env*\n"
	if err := os.WriteFile(path, []byte(gitignore), 0644); err != nil {
		b.Fatal(err)
	}
	ig := loadIgnoreRules(path)
	paths := []string{
		"lib", "lib/models", "lib/models/concerns", "ui/views/admin",
		"public/assets", "tmp", "vendor/bundle", "cmd",
	}
	b.ReportAllocs()
	for b.Loop() {
		for _, p := range paths {
			ig.skip(p)
		}
	}
}

// BenchmarkMatchEvent measures the work the watch loop does for each
// file event: it makes the path relative, then matches each process.
func BenchmarkMatchEvent(b *testing.B) {
	defs, err := parseProcfile(strings.NewReader(benchProcfile))
	if err != nil {
		b.Fatal(err)
	}
	mgr := &manager{watchDir: "/Users/dev/src/app"}
	for _, d := range defs {
		if len(d.watchPatterns) > 0 {
			mgr.watched = append(mgr.watched, &process{name: d.name, watchPatterns: d.watchPatterns})
		}
	}
	events := []string{
		"/Users/dev/src/app/lib/models/user.rb",
		"/Users/dev/src/app/ui/views/admin/index.haml",
		"/Users/dev/src/app/public/assets/app.css",
	}
	b.ReportAllocs()
	for b.Loop() {
		for _, e := range events {
			rel := mgr.relPath(e)
			for _, proc := range mgr.watched {
				mgr.matchPatterns(proc.watchPatterns, rel)
			}
		}
	}
}

func TestIgnoreRulesSkip(t *testing.T) {
	tests := []struct {
		name      string
		gitignore string
		path      string
		want      bool
	}{
		{
			name: ".git is always pruned",
			path: ".git",
			want: true,
		},
		{
			name: "node_modules is pruned unnamed",
			path: "node_modules",
			want: true,
		},
		{
			name: "nested node_modules is pruned",
			path: "ui/node_modules",
			want: true,
		},
		{
			name:      "source is kept",
			gitignore: "tmp\n",
			path:      "lib/models",
			want:      false,
		},
		{
			name:      "unanchored rule matches any segment",
			gitignore: "tmp\n",
			path:      "lib/tmp",
			want:      true,
		},
		{
			name:      "anchored rule matches only at root",
			gitignore: "/tmp\n",
			path:      "lib/tmp",
			want:      false,
		},
		{
			name:      "negation re-includes",
			gitignore: "build\n!build/keep\n",
			path:      "build/keep",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)

			path := filepath.Join(t.TempDir(), ".gitignore")
			if tt.gitignore != "" {
				is.NoErr(os.WriteFile(path, []byte(tt.gitignore), 0644))
			}

			is.Eq(loadIgnoreRules(path).skip(tt.path), tt.want)
		})
	}
}

func TestProcmanIntegration(t *testing.T) {
	is := is.New(t)

	content := "echo: echo 'hello'\nsleep: sleep 10"
	is.NoErr(os.WriteFile("Procfile.dev", []byte(content), 0644))
	defer os.Remove("Procfile.dev")

	cmd := exec.Command("./procman", "echo,sleep")
	is.NoErr(cmd.Start())

	time.Sleep(1 * time.Second)

	is.NoErr(cmd.Process.Signal(os.Interrupt))
	is.NoErr(cmd.Wait())
}

func TestRestartOnFileChange(t *testing.T) {
	is := is.New(t)

	dir := t.TempDir()
	procfile := "echo: echo run; sleep 5  # watch: *.rb\n"
	is.NoErr(os.WriteFile(filepath.Join(dir, "Procfile.dev"), []byte(procfile), 0644))
	// Create an initial .rb file so the watcher has something to watch
	is.NoErr(os.WriteFile(filepath.Join(dir, "init.rb"), []byte(""), 0644))

	cwd, err := os.Getwd()
	is.NoErr(err)
	exe := filepath.Join(cwd, "procman")
	cmd := exec.Command(exe, "echo")
	cmd.Dir = dir
	stdout, err := cmd.StdoutPipe()
	is.NoErr(err)
	is.NoErr(cmd.Start())
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}()

	sc := bufio.NewScanner(stdout)
	watching := make(chan struct{})
	restarted := make(chan struct{})
	var mu sync.Mutex
	var lines []string
	seen := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(lines, "\n")
	}
	go func() {
		for sc.Scan() {
			line := sc.Text()
			mu.Lock()
			lines = append(lines, line)
			mu.Unlock()
			if strings.Contains(line, "watching ") {
				close(watching)
			}
			if strings.Contains(line, "restarting...") {
				close(restarted)
				return
			}
		}
		_ = sc.Err()
	}()

	// Wait for the watch to exist before writing. A write that lands
	// first produces no event and nothing writes again, so a sleep here
	// is a guess at how long a loaded box takes to walk the tree, and
	// the test fails for the one reason it is not looking for.
	select {
	case <-watching:
	case <-time.After(10 * time.Second):
		t.Fatalf("timeout waiting for watcher setup; procman said:\n%s", seen())
	}

	// Trigger a change by modifying the existing file
	is.NoErr(os.WriteFile(filepath.Join(dir, "init.rb"), []byte("puts :x\n"), 0644))

	select {
	case <-restarted:
	case <-time.After(10 * time.Second):
		t.Fatalf("timeout waiting for restart; procman said:\n%s", seen())
	}
}
