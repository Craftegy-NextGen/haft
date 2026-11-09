# Windows prerequisites

This page lists what a Windows machine needs to build haft, run its tests, run the FPF refresh workflow
(`cmd/fpf-refresh`) and run the FPF query token gate. Commands below are run from Git Bash unless they say
otherwise.

## Tools

| Tool | Why | Notes |
|------|-----|-------|
| Go 1.25 | build and test | the version in `go.mod` (`go 1.25.8`); an older Go downloads the matching toolchain when `GOTOOLCHAIN=auto` |
| A C compiler for cgo | `github.com/smacker/go-tree-sitter` compiles C sources | MinGW-w64 gcc on `PATH`, with `CGO_ENABLED=1` and `CC` naming it when it is not the first `gcc` found |
| Git for Windows | `bash` for the scripts, `git` for the refresh workflow | install it with its default options so `bin\bash.exe` exists |
| `uv` | runs the token gate without a system Python | see below |

No system Python is needed. `uv` finds a CPython 3.10 to 3.13 on the machine, or downloads one.

## Installing uv

Use a package manager:

```
winget install --id=astral-sh.uv -e
```

```
scoop install main/uv
```

Or download a release archive from the uv releases page on GitHub, compare its SHA-256 with the checksum published
next to it, and unpack it into a folder on `PATH`:

```
sha256sum uv-x86_64-pc-windows-msvc.zip
```

The token gate script does not install uv, and this page does not recommend piping a downloaded script into a shell.
The script needs uv 0.13.0 or newer (the minimum is set in `scripts/fpf_query_token_gate_uv.env`; it was exercised
with 0.13.7) and stops with a message when uv is missing or older.

## Where uv keeps its files

uv stores downloads and Python builds in its own folders. Point them at a drive of your choice with environment
variables before the first run; the token gate script reads them and never sets or overrides them:

| Variable | Holds |
|----------|-------|
| `UV_CACHE_DIR` | downloaded package wheels and build caches |
| `UV_PYTHON_INSTALL_DIR` | CPython builds that uv downloads (about 70 MB each) |
| `UV_TOOL_DIR` | tools installed with `uv tool` (not used by the gate) |

The other uv settings (`UV_INDEX_URL`, `UV_PYTHON_DOWNLOADS`, a `uv.toml` file) apply as well. Package hashes are
checked whatever index is used.

The gate builds its virtual environment in a temporary directory (`TMPDIR`, else `/tmp`) and removes it on exit.

## Network access on the first run

A cold run reaches two places:

- the package index (PyPI by default) for the pinned `tiktoken` wheels, installed with `--require-hashes` from
  `scripts/fpf_query_token_count.requirements.txt`;
- the host of the `o200k_base` tokenizer asset, which the helper downloads once and verifies against a fixed SHA-256.

The tokenizer asset is cached in the folder named by `TIKTOKEN_CACHE_DIR`. Set it to a path with a drive letter and
forward slashes to keep it where you want it, and the second run needs no network for it. When a
managed CPython has to be downloaded, that goes to `UV_PYTHON_INSTALL_DIR`. An offline run works only when all of
these caches are already filled.

## Running the token gate

From the repository root:

```
bash scripts/fpf_query_token_gate_uv.sh
```

The script creates a virtual environment with `uv venv`, installs the locked packages with
`uv pip install --require-hashes --only-binary :all:`, then runs two Go tests with the build tag
`query_token_gate` and `HAFT_QUERY_TOKEN_GATE_PYTHON` pointing at that interpreter:

- `internal/cli` `TestFPFQueryWorkingViewEmbeddedO200kAcceptance`
- `internal/fpf` `TestFPFQueryWorkingViewSyntheticTokenCalculus`

Each must print its exact `--- PASS` line. The Go tests need the same environment as any other test run (Go and the C
compiler on `PATH`). `bash scripts/fpf_query_token_gate_uv.sh --print-bootstrap-python` prints the installed
CPython in the supported range that uv would pick, and exits with an error when there is none (a run would then
download one).

`scripts/fpf_query_token_gate.sh` is the upstream script. It builds the environment with `python -m venv` and calls
`bin/python`, which a Windows environment does not have (it has `Scripts\python.exe`), so it does not run on Windows.
The uv script runs the same tests and has the same exit-code conventions (2 for bad usage, 1 for any failure).

## Running the FPF refresh workflow

```
go run ./cmd/fpf-refresh verify --repo .
go run ./cmd/fpf-refresh check --repo .
```

On Windows `check` and `apply` run the token gate through `scripts/fpf_query_token_gate_uv.sh` and need a `bash.exe`
to run it. They take the first `bash` on `PATH`; when there is none, they look for `bin\bash.exe` in the Git for
Windows installation that holds the `git` on `PATH`. The `bash.exe` of the Windows Subsystem for Linux
(`C:\Windows\System32\bash.exe`) is skipped. When no bash is found, the check still completes and reports a
`token_gate_failed` diagnostic that says so; put the `bin` folder of Git for Windows on `PATH` to fix it.

## Building the embedding sidecar (haft-embed) on Windows

The semantic recall sidecar is a Rust program (`embed-sidecar`). The prebuilt static ONNX Runtime library that the
`ort` crate downloads by default needs C++ standard library symbols that older Visual Studio toolsets (for example
MSVC 14.40) do not have, so a plain `cargo build` fails at the link step with `LNK1120`. Microsoft's own ONNX Runtime
release links as an import library and needs none of them. No Cargo feature change is needed, only environment
variables for the build.

1. Download `onnxruntime-win-x64-<version>.zip` from the ONNX Runtime GitHub releases (version 1.24.x; 1.24.4 was
   verified). Check its SHA-256 against the digest GitHub shows for the asset, and unpack it into a new folder.
2. Build with the variables set only for that command:

   ```
   ORT_LIB_LOCATION=<unpacked folder>\lib ORT_PREFER_DYNAMIC_LINK=1 cargo build --manifest-path embed-sidecar/Cargo.toml
   ```

3. Copy `onnxruntime.dll` and `onnxruntime_providers_shared.dll` from that `lib` folder next to `haft-embed.exe`. Putting
   the folder on `PATH` is not enough: the Windows system folder holds a stub `onnxruntime.dll` that the loader finds
   first.

The shared sidecar listens on a Unix domain socket under `%LOCALAPPDATA%\haft\embed` (Windows 10 version 1803 or
later). A Windows socket path may be at most 107 bytes, so a very long `%LOCALAPPDATA%` makes haft fall back to the
per-process stdio sidecar. The first start downloads the embedding model (roughly 300 to 440 MB depending on the
model) into the cache directory.
