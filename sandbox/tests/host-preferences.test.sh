#!/usr/bin/env bash
set -euo pipefail
script_dir="$(cd -- "$(dirname -- "$0")" && pwd)"
"${CENCI_TEST_PYTHON:-/usr/bin/python3}" -B "$script_dir/host_preferences_test.py"
