package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
)

// scanLines reads lines from an input reader and applies a callback function to
// each line. It is used for processing the contents of the procfile.
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

// parseProcfile reads and parses the procfile, returning a slice of entries.
func parseProcfile(filename string) (entries []entry, err error) {
	file, err := os.Open(procfile)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	re, _ := regexp.Compile(`^([\w-]+):\s+(.+)$`)
	names := make(map[string]bool)
	dup := ""

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
			dup = name
			return false
		}
		names[name] = true
		entries = append(entries, entry{name: name, cmd: cmd})

		return true
	})
	if err != nil {
		return nil, err
	}
	if dup != "" {
		return nil, fmt.Errorf("Duplicate process name %s in Procfile.dev", dup)
	}
	if len(entries) == 0 {
		return nil, errors.New("No entries found in Procfile.dev")
	}

	return entries, nil
}
