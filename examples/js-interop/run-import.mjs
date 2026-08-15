// Run a JabaScript program from Node, supplying an imported function.
//
//   jabac callback.jaba            # writes callback.wat
//   wat2wasm callback.wat          # JS needs the binary format
//   node run.mjs
//
// The module imports two things: the WASI functions the embedded runtime
// uses for printing/exit (node:wasi provides those), and everything the
// program declared as `extern fn` that the runtime doesn't define, which
// arrives under the "env" key below.
import { readFile } from "node:fs/promises";
import { WASI } from "node:wasi";

const wasi = new WASI({ version: "preview1" });
const bytes = await readFile(new URL("./import.wasm", import.meta.url));
const module = await WebAssembly.compile(bytes);
const instance = await WebAssembly.instantiate(module, {
  ...wasi.getImportObject(),
  env: {
    // extern fn js_pow(base: i32, exp: i32) -> i32
    js_pow: (base, exp) => base ** exp,
  },
});
process.exit(wasi.start(instance));
