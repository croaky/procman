package main

import (
	"fmt"
	"os"
	"time"
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

	entries, err := readProcfile(procfile)
	check(err)

	mgr := &manager{}
	mgr.output = &output{}
	err = mgr.setupProcesses(entries, procNames)
	check(err)

	mgr.SetupSignalHandling()

	for _, proc := range mgr.procs {
		mgr.runProcess(proc)
	}

	go mgr.waitForExit()
	mgr.procWg.Wait()
}
