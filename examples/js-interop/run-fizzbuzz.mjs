// Call a JabaScript-exported function from JavaScript.
//
//   jabac export.jaba            # writes export.wat
//   wat2wasm export.wat          # JS needs the binary format
//   node run-export.mjs
//
// The module still imports the WASI functions the embedded runtime uses for
// printing/exit (node:wasi provides those), but we never call wasi.start(),
// so _start — and therefore main and proc_exit — never runs. The exported
// functions are reached directly through the instance instead.
import { readFile } from "node:fs/promises";
import { WASI } from "node:wasi";

const wasi = new WASI({ version: "preview1" });
const bytes = await readFile(new URL("./fizzbuzz.wasm", import.meta.url));
const module = await WebAssembly.compile(bytes);
const instance = await WebAssembly.instantiate(module, wasi.getImportObject());
wasi.start(instance);

const { fizz_buzz } = instance.exports;
fizz_buzz();
