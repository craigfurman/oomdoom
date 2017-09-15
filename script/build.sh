#!/usr/bin/env bash
set -euo pipefail

base=$(cd "$(dirname "$0")/.." && pwd)
gcc -o grabmem/grabmem grabmem/grabmem.c
(
cd "$base"
go install
)
