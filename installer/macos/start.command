#!/bin/sh
# Terminal runs this when Digital Commons.app opens it.
cd "$(dirname "$0")" || exit 1
exec ./runtime/bin/python3 dc.py start
