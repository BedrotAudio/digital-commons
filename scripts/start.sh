#!/bin/sh
# Start the gateway and open the terminal. The gateway stops when the terminal closes.
# A wrapper over: python3 dc.py dev
# Extra arguments are passed through. Run it with: sh scripts/start.sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

for candidate in python3 python; do
    if command -v "$candidate" >/dev/null 2>&1 && "$candidate" -c 'import sys' >/dev/null 2>&1; then
        exec "$candidate" "$root/dc.py" dev "$@"
    fi
done

echo "Python was not found. Digital Commons needs Python 3.11 or newer." >&2
echo "Install it with your package manager, for example: sudo apt install python3 python3-venv" >&2
echo "or on macOS: brew install python@3.12" >&2
exit 1
