#!/usr/bin/env bash
# FPF Query token gate through uv: same two exact-anchor Go tests as scripts/fpf_query_token_gate.sh, with the
# virtual environment built by uv instead of python -m venv and pip. Works where that script cannot (Windows, where
# a virtual environment keeps its interpreter under Scripts/ and not bin/) and needs no CPython on the machine in
# advance: uv finds or downloads one in the supported range. See WINDOWS.md.
#
# uv is never installed by this script. uv's own settings are respected (UV_CACHE_DIR, UV_PYTHON_INSTALL_DIR,
# UV_PYTHON_DOWNLOADS, UV_INDEX_URL, uv.toml); none is set or overridden here.

set -euo pipefail

token_gate_script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
token_gate_repo_root="$(cd -- "$token_gate_script_dir/.." && pwd -P)"
token_gate_settings_file="$token_gate_script_dir/fpf_query_token_gate_uv.env"
token_gate_mode="run"

case "$#:${1:-}" in
  "0:") ;;
  "1:--print-bootstrap-python") token_gate_mode="print-bootstrap-python" ;;
  *)
    echo >&2 "usage: scripts/fpf_query_token_gate_uv.sh [--print-bootstrap-python]"
    exit 2
    ;;
esac

read_token_gate_setting() {
  local token_gate_key="$1"
  local token_gate_value

  token_gate_value="$(
    sed -n "s/^${token_gate_key}=//p" "$token_gate_settings_file" | head -n 1 | tr -d '\r'
  )"
  if [[ -z "$token_gate_value" ]]; then
    echo >&2 "FPF Query token gate (uv): ${token_gate_key} is missing in $token_gate_settings_file"
    exit 1
  fi
  printf '%s' "$token_gate_value"
}

# Windows tools (uv, go, python) take Windows paths; elsewhere this is the identity.
native_token_gate_path() {
  if command -v cygpath >/dev/null 2>&1; then
    cygpath -m -- "$1"
  else
    printf '%s' "$1"
  fi
}

# Succeeds when version $1 is at least version $2 (dotted numeric fields).
token_gate_version_at_least() {
  local -a token_gate_have token_gate_want
  local token_gate_index token_gate_h token_gate_w

  IFS=. read -r -a token_gate_have <<<"$1"
  IFS=. read -r -a token_gate_want <<<"$2"
  for ((token_gate_index = 0; token_gate_index < ${#token_gate_want[@]}; token_gate_index++)); do
    token_gate_h="${token_gate_have[token_gate_index]:-0}"
    token_gate_w="${token_gate_want[token_gate_index]}"
    if ((10#$token_gate_h > 10#$token_gate_w)); then
      return 0
    fi
    if ((10#$token_gate_h < 10#$token_gate_w)); then
      return 1
    fi
  done
  return 0
}

token_gate_python_min="$(read_token_gate_setting FPF_GATE_PYTHON_MIN)"
token_gate_python_below="$(read_token_gate_setting FPF_GATE_PYTHON_BELOW)"
token_gate_uv_min_version="$(read_token_gate_setting FPF_GATE_UV_MIN_VERSION)"
token_gate_python_request="cpython>=${token_gate_python_min},<${token_gate_python_below}"

if ! command -v uv >/dev/null 2>&1; then
  echo >&2 \
    "FPF Query token gate (uv): uv was not found on PATH; install uv >= ${token_gate_uv_min_version} (see WINDOWS.md)"
  exit 1
fi

token_gate_uv_version="$(uv --version | sed -n 's/^uv \([0-9][0-9.]*\).*/\1/p')"
if [[ -z "$token_gate_uv_version" ]] ||
  ! token_gate_version_at_least "$token_gate_uv_version" "$token_gate_uv_min_version"; then
  echo >&2 \
    "FPF Query token gate (uv): uv ${token_gate_uv_version:-of unknown version} is older than the required ${token_gate_uv_min_version} (see WINDOWS.md)"
  exit 1
fi

if [[ "$token_gate_mode" == "print-bootstrap-python" ]]; then
  # The interpreter uv would use from what is already installed. Without one, a run downloads a managed CPython.
  if ! uv python find "$token_gate_python_request"; then
    echo >&2 \
      "FPF Query token gate (uv): no installed CPython ${token_gate_python_min} up to ${token_gate_python_below} (excluded); a run downloads a managed one"
    exit 1
  fi
  exit 0
fi

token_gate_env="$(mktemp -d "${TMPDIR:-/tmp}/haft-query-token-gate.XXXXXX")"

cleanup_token_gate_env() {
  rm -rf -- "$token_gate_env"
}

trap cleanup_token_gate_env EXIT HUP INT TERM

cd -- "$token_gate_repo_root"

echo >&2 \
  "FPF Query token gate (uv): a cold run needs package-index and o200k_base asset network access; package wheels and the BPE asset are SHA-256 verified."

uv venv --quiet --python "$token_gate_python_request" "$(native_token_gate_path "$token_gate_env")"

if [[ -f "$token_gate_env/Scripts/python.exe" ]]; then
  token_gate_python="$token_gate_env/Scripts/python.exe"
else
  token_gate_python="$token_gate_env/bin/python"
fi
token_gate_native_python="$(native_token_gate_path "$token_gate_python")"

env -u PYTHONHOME -u PYTHONPATH \
  "$token_gate_native_python" -c '
import platform
import sys

low = tuple(int(part) for part in sys.argv[1].split("."))
below = tuple(int(part) for part in sys.argv[2].split("."))
supported = (
    platform.python_implementation() == "CPython"
    and low <= sys.version_info[: len(low)]
    and sys.version_info[: len(below)] < below
)
if not supported:
    raise SystemExit(
        "FPF Query token gate requires CPython "
        + sys.argv[1]
        + " up to "
        + sys.argv[2]
        + " (excluded); got "
        + platform.python_implementation()
        + " "
        + platform.python_version()
    )
' "$token_gate_python_min" "$token_gate_python_below"

env -u PYTHONHOME -u PYTHONPATH \
  uv pip install \
  --quiet \
  --python "$token_gate_native_python" \
  --require-hashes \
  --only-binary :all: \
  --requirement "$(native_token_gate_path "$token_gate_repo_root/scripts/fpf_query_token_count.requirements.txt")"

run_exact_token_gate_test() {
  local token_gate_package="$1"
  local token_gate_test_name="$2"
  local token_gate_log="$token_gate_env/${token_gate_test_name}.log"

  env -u PYTHONHOME -u PYTHONPATH \
    HAFT_QUERY_TOKEN_GATE_PYTHON="$token_gate_native_python" \
    go test \
    -tags=query_token_gate \
    -count=1 \
    -v \
    "$token_gate_package" \
    -run "^${token_gate_test_name}$" \
    | tee "$token_gate_log"

  if grep -Fq '[no tests to run]' "$token_gate_log"; then
    echo >&2 "FPF Query token gate anchor disappeared: $token_gate_package/$token_gate_test_name"
    return 1
  fi

  if ! grep -Fq -- "--- PASS: ${token_gate_test_name} " "$token_gate_log"; then
    echo >&2 "FPF Query token gate did not observe exact PASS anchor: $token_gate_package/$token_gate_test_name"
    return 1
  fi
}

run_exact_token_gate_test \
  ./internal/cli \
  TestFPFQueryWorkingViewEmbeddedO200kAcceptance

run_exact_token_gate_test \
  ./internal/fpf \
  TestFPFQueryWorkingViewSyntheticTokenCalculus
