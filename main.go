package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

var (
	colors     = []int{2, 3, 4, 5, 6, 42, 130, 103, 129, 108}
	procfile   = "./Procfile.dev"
	timeout    = 5 * time.Second
	procfileRe = regexp.MustCompile(`^([\w-]+):\s+(.+)$`)
)

// entry represents a single line in the procfile, with a name and command
type entry struct {
	name string
	cmd  string
}

func main() {
	procNames, err := parseArgs(os.Args)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	entries, err := readProcfile(procfile)
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
func readProcfile(filename string) ([]entry, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return parseProcfile(file)
}

// parseProcfile reads and parses the procfile, returning a slice of entries.
func parseProcfile(r io.Reader) ([]entry, error) {
	names := make(map[string]bool)
	var entries []entry

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		params := procfileRe.FindStringSubmatch(line)
		if len(params) != 3 {
			continue
		}

		name, cmd := params[1], params[2]
		if names[name] {
			return nil, fmt.Errorf("Duplicate process name %s in Procfile.dev", name)
		}
		names[name] = true
		entries = append(entries, entry{name: name, cmd: cmd})
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, errors.New("No entries found in Procfile.dev")
	}
	return entries, nil
}
