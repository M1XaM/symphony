# Symphony

Draw a topology of microservices, run it as real Docker containers, drive
load through it, inject faults and compare how orchestration and
choreography behave under stress.

| file | what it is |
|------|------------|
| `symphony.exe` | the app: a window with the map, the inspector and the tests |
| `symphony-cli.exe` | the same app from a terminal (Command Prompt or PowerShell), on the same state and containers |

Keep the two in one folder: they share their state in `symphony.db`, which
appears next to them on the first run.

All you need is **Docker Desktop**, installed and running. Nothing else
needs installing: double-click `symphony.exe`.

    symphony-cli --help           every command
    symphony-cli generate-guide   writes GUIDE.md here: workflows, examples, every command

More: https://github.com/M1XaM/symphony
