Symphony for Windows
====================

  symphony.exe       the app: draw a topology, run it as Docker containers,
                     drive load, inject faults, run the test scenarios
  symphony-cli.exe   the same app from a terminal (Command Prompt or
                     PowerShell); "symphony-cli --help" lists the commands,
                     "symphony-cli <command> --help" explains one
  GUIDE.md           the command line's guide: workflows with examples,
                     every command, the fault kinds and the scenarios. It
                     is also what to give an agentic model that is to drive
                     symphony-cli ("symphony-cli --json-help" prints the
                     same catalogue as JSON)

Keep the two files in the same folder: they share their state in
symphony.db, which is created next to them on the first run. Remove it to
make Symphony forget every deployment.

Requirements
------------

- Windows 10 or 11, 64-bit.
- Docker Desktop, installed and running. Every topology runs as Docker
  containers. Without Docker the app still opens and says Docker is
  missing; it connects by itself once Docker Desktop is started.
- The Microsoft Edge WebView2 runtime, which ships with Windows 10/11. If
  it is missing, symphony.exe offers to download it.

Nothing needs installing: double-click symphony.exe. The first Apply builds
the five block images, which takes a minute or two; later runs reuse them.

More: https://github.com/M1XaM/symphony
