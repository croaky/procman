package main

import (
	"fmt"
	"strings"
)

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
