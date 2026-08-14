// jabac is the JabaScript compiler: it lowers a .jaba source file to a
// textual WebAssembly module (.wat) that any WASI runtime can execute.
//
//	jabac hello.jaba          # writes hello.wat
//	wasmtime hello.wat
//
//	jabac -run hello.jaba     # compile and run in one step (needs wasmtime)
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"jabascript/internal/compile"
)

func main() {
	out := flag.String("o", "", "output file (default: input with .wat extension)")
	run := flag.Bool("run", false, "run the compiled module with wasmtime")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: jabac [-o out.wat] [-run] file.jaba\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	input := flag.Arg(0)

	src, err := os.ReadFile(input)
	if err != nil {
		fatal("%v", err)
	}

	wat, err := compile.Compile(string(src))
	if err != nil {
		fatal("%s: %v", input, err)
	}

	outPath := *out
	if outPath == "" {
		outPath = strings.TrimSuffix(input, ".jaba") + ".wat"
	}
	if err := os.WriteFile(outPath, []byte(wat), 0o644); err != nil {
		fatal("%v", err)
	}

	if *run {
		cmd := exec.Command("wasmtime", outPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				os.Exit(exit.ExitCode())
			}
			fatal("running wasmtime: %v", err)
		}
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "jabac: "+format+"\n", args...)
	os.Exit(1)
}
