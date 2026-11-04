@echo off
rem Digital Commons task runner for Windows. Type "dc dev", "dc demo", "dc setup" and so on.
rem It finds Python and runs dc.py from the folder this file is in.
setlocal

set "DC_SCRIPT=%~dp0dc.py"

rem The Microsoft Store placeholder is also called python.exe and opens the Store
rem instead of running, so check that the command really runs Python.
python -c "import sys" >nul 2>nul
if not errorlevel 1 (
    python "%DC_SCRIPT%" %*
    exit /b %ERRORLEVEL%
)

rem The python.org installer can leave Python off PATH and register the py launcher.
py -3 -c "import sys" >nul 2>nul
if not errorlevel 1 (
    py -3 "%DC_SCRIPT%" %*
    exit /b %ERRORLEVEL%
)

echo Python was not found. Digital Commons needs Python 3.11 or newer.
echo Install it with:
echo.
echo   winget install Python.Python.3.12
echo.
echo Then open a new terminal window and run this command again.
exit /b 1
