#!/bin/sh
# Digital Commons.app runs this. Digital Commons works in Terminal, so all the app does
# is open a Terminal window for it.
resources="$(cd "$(dirname "$0")/../Resources" && pwd)" || exit 1
bundle="$(cd "$resources/../.." && pwd)"
# Opening the app was the person's choice, made in System Settings for an unsigned app.
# Clear the downloaded-file flag, which would otherwise make macOS block each program
# inside the app in turn.
/usr/bin/xattr -dr com.apple.quarantine "$bundle" 2>/dev/null
exec /usr/bin/open -a Terminal "$resources/app/start.command"
