package codegen

// runtimeWAT is the JabaScript runtime, hand-written in WAT and embedded in
// every emitted module — the Wasm analog of the AArch64 backend's
// runtime/jaba_rt.c shim that gets linked alongside every program.
//
// It provides the four extern fns of the standard runtime on top of raw
// WASI: printing (via fd_write to stdout) and a bump allocator (free is a
// no-op, exactly as honest as it sounds). Scratch memory below address 96
// is reserved for the iovec, fd_write's out-parameter, and the itoa buffer;
// this is safe because nothing else is ever placed below 1024.
const runtimeWAT = `
  ;; ---- runtime

  ;; write_all(buf, len) — write the whole range to stdout via fd_write,
  ;; retrying when the host performs a short write. Gives up on an error
  ;; (nonzero errno) or a zero-byte write, so it always terminates.
  (func $rt_write_all (param $buf i32) (param $len i32)
    (local $n i32)
    block $done
      loop $retry
        local.get $len
        i32.eqz
        br_if $done
        ;; iovec = { buf, len }
        i32.const 8
        local.get $buf
        i32.store
        i32.const 12
        local.get $len
        i32.store
        i32.const 1   ;; fd 1 = stdout
        i32.const 8   ;; iovec*
        i32.const 1   ;; iovec count
        i32.const 16  ;; nwritten out-param
        call $fd_write
        br_if $done   ;; nonzero errno: nothing sensible left to do
        ;; advance past the bytes that were written
        i32.const 16
        i32.load
        local.tee $n
        i32.eqz
        br_if $done
        local.get $buf
        local.get $n
        i32.add
        local.set $buf
        local.get $len
        local.get $n
        i32.sub
        local.set $len
        br $retry
      end
    end
  )

  ;; jaba_print_str(s: *u8) — write the NUL-terminated string at s to stdout.
  (func $rt_jaba_print_str (param $s i32)
    (local $p i32)
    ;; find the NUL
    local.get $s
    local.set $p
    block $done
      loop $scan
        local.get $p
        i32.load8_u
        i32.eqz
        br_if $done
        local.get $p
        i32.const 1
        i32.add
        local.set $p
        br $scan
      end
    end
    local.get $s
    local.get $p
    local.get $s
    i32.sub
    call $rt_write_all
  )

  ;; jaba_print_int(v: i64) — write v in decimal to stdout.
  (func $rt_jaba_print_int (param $v i64)
    (local $p i32)
    (local $n i64)
    ;; digits are written backwards from the end of the scratch buffer [32,96)
    i32.const 96
    local.set $p
    local.get $v
    local.set $n
    local.get $v
    i64.const 0
    i64.lt_s
    if
      ;; magnitude as unsigned; correct even for the most negative i64
      i64.const 0
      local.get $v
      i64.sub
      local.set $n
    end
    loop $digits
      local.get $p
      i32.const 1
      i32.sub
      local.set $p
      local.get $p
      local.get $n
      i64.const 10
      i64.rem_u
      i32.wrap_i64
      i32.const 48  ;; '0'
      i32.add
      i32.store8
      local.get $n
      i64.const 10
      i64.div_u
      local.set $n
      local.get $n
      i64.const 0
      i64.ne
      br_if $digits
    end
    local.get $v
    i64.const 0
    i64.lt_s
    if
      local.get $p
      i32.const 1
      i32.sub
      local.set $p
      local.get $p
      i32.const 45  ;; '-'
      i32.store8
    end
    local.get $p
    i32.const 96
    local.get $p
    i32.sub
    call $rt_write_all
  )

  ;; malloc(n: u32) -> *u8 — bump allocator: round n up to 16 bytes, grow
  ;; memory when the heap outruns it. Returned blocks are 16-byte aligned.
  (func $rt_malloc (param $n i32) (result i32)
    (local $p i32)
    (local $end i32)
    global.get $__heap
    local.set $p
    ;; end = p + ((n + 15) & -16); trap if either step wraps past 2^32,
    ;; which would silently move the heap cursor backwards
    local.get $n
    i32.const 15
    i32.add
    i32.const -16
    i32.and
    local.tee $end
    local.get $n
    i32.lt_u      ;; rounded size < n means n + 15 overflowed
    if
      unreachable  ;; allocation too large
    end
    local.get $p
    local.get $end
    i32.add
    local.tee $end
    local.get $p
    i32.lt_u      ;; end < p means p + size overflowed
    if
      unreachable  ;; allocation too large
    end
    block $fits
      local.get $end
      memory.size
      i32.const 16
      i32.shl
      i32.le_u
      br_if $fits
      ;; grow by however many 64 KiB pages we are short
      local.get $end
      memory.size
      i32.const 16
      i32.shl
      i32.sub
      i32.const 65535
      i32.add
      i32.const 16
      i32.shr_u
      memory.grow
      i32.const -1
      i32.ne
      br_if $fits
      unreachable  ;; out of memory
    end
    local.get $end
    global.set $__heap
    local.get $p
  )

  ;; free(p: *u8) — a bump allocator never frees.
  (func $rt_free (param $p i32))
`
