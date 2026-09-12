//go:build ignore

// Command direction_base_main prints sanitize.String's output for every input
// in a file of strconv.Quote'd lines, one strconv.Quote'd output per line.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"

	"github.com/LerianStudio/lib-commons/v7/commons/security/sanitize"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: direction_base_main <inputs.txt> <base-<sha>.txt>")
		os.Exit(2)
	}

	in, err := os.Open(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer in.Close()

	out, err := os.Create(os.Args[2])
	if err != nil {
		panic(err)
	}
	defer out.Close()

	// A base file that is silently short is a gate that silently passes: a
	// flush that fails (full disk) has to stop the run, not truncate the oracle.
	w := bufio.NewWriter(out)
	defer func() {
		if err := w.Flush(); err != nil {
			panic(err)
		}
	}()

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<22)

	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		s, err := strconv.Unquote(line)
		if err != nil {
			panic(fmt.Sprintf("unquote %q: %v", line, err))
		}
		fmt.Fprintln(w, strconv.Quote(sanitize.String(s)))
	}
	if err := sc.Err(); err != nil {
		panic(err)
	}
}
