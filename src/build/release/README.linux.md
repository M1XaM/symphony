# Symphony

Draw a topology of microservices, run it as real Docker containers, drive
load through it, inject faults and compare how orchestration and
choreography behave under stress.

| file | what it is |
|------|------------|
| `symphony` | the app: a window with the map, the inspector and the tests |
| `symphony-cli` | the same app from a terminal, on the same state and containers |

Keep the two in one folder: they share their state in `symphony.db`, which
appears next to them on the first run.

All you need is **Docker**, installed and running. (The window uses GTK 3
and WebKitGTK 4.1, which desktop distributions ship.)

    ./symphony                      # or double-click it
    ./symphony-cli --help           # every command
    ./symphony-cli generate-guide   # writes GUIDE.md here: workflows, examples, every command

More: https://github.com/M1XaM/symphony
