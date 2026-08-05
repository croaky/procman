package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)

			mgr := &manager{watchDir: ""} // empty means path is already relative
			got := mgr.matchPatterns(tt.patterns, tt.path)

			is.Eq(got, tt.want)
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
	restarted := make(chan struct{})
	go func() {
		for sc.Scan() {
			if strings.Contains(sc.Text(), "restarting...") {
				close(restarted)
				return
			}
		}
		_ = sc.Err()
	}()

	// Trigger a change by modifying the existing file
	time.Sleep(500 * time.Millisecond)
	is.NoErr(os.WriteFile(filepath.Join(dir, "init.rb"), []byte("puts :x\n"), 0644))

	select {
	case <-restarted:
	case <-time.After(4 * time.Second):
		t.Fatal("timeout waiting for restart")
	}
}
