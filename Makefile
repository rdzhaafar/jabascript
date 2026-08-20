GO_SOURCES := $(shell find cmd internal -name '*.go' ! -name '*_test.go')

NODE := node --disable-warning=ExperimentalWarning

jabac: $(GO_SOURCES) $(wildcard go.mod go.sum)
	go build -o jabac ./cmd/jabac/main.go

.PHONY: hello
hello: jabac
	./jabac -run examples/hello.jaba

.PHONY: fib
fib: jabac
	./jabac -run examples/fib.jaba

.PHONY: list
list: jabac
	./jabac -list examples/list.jaba

.PHONY: point
point: jabac
	./jabac -run examples/point.jaba

.PHONY: sort
sort: jabac
	./jabac -run examples/sort.jaba

.PHONY: export
export: jabac
	./jabac examples/js-interop/export.jaba
	wat2wasm examples/js-interop/export.wat -o examples/js-interop/export.wasm
	$(NODE) examples/js-interop/run-export.mjs

.PHONY: import
import: jabac
	./jabac examples/js-interop/import.jaba
	wat2wasm examples/js-interop/import.wat -o examples/js-interop/import.wasm
	$(NODE) examples/js-interop/run-import.mjs

.PHONY: fizzbuzz
fizzbuzz: jabac
	./jabac examples/js-interop/fizzbuzz.jaba
	wat2wasm examples/js-interop/fizzbuzz.wat -o examples/js-interop/fizzbuzz.wasm
	$(NODE) examples/js-interop/run-fizzbuzz.mjs

.PHONY: test
test:
	go test -count=1 -v ./...

.PHONY: help
help:
	@printf "usage: make [target]\n\n"
	@printf "targets:\n"
	@printf "  jabac     build the compiler\n"
	@printf "  hello     compile and run examples/hello.jaba\n"
	@printf "  fib       compile and run examples/fib.jaba\n"
	@printf "  list      compile and run examples/list.jaba\n"
	@printf "  point     compile and run examples/point.jaba\n"
	@printf "  sort      compile and run examples/sort.jaba\n"
	@printf "  export    compile the js-interop export example and run it in Node\n"
	@printf "  import    compile the js-interop import example and run it in Node\n"
	@printf "  fizzbuzz  compile the js-interop fizzbuzz example and run it in Node\n"
	@printf "  test      run the test suite\n"
	@printf "  help      show this message\n"
