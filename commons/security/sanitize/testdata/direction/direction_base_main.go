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

	w := bufio.NewWriter(out)
	defer w.Flush()

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
