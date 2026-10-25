# SAS4 Bulk (Go)

A Go port of the Python `sas4_bulk/` app. By default it opens as a desktop
window (built with [Wails](https://wails.io) v2). The same UI also runs in a
browser with `--web`. It is a single program with the UI built in, so the
host needs no Python.

## Build

Install the Wails CLI once:

    go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0
    wails doctor

Then build from this folder:

| Platform | Command | Output |
|---|---|---|
| macOS | `wails build -platform darwin/universal` | `build/bin/SAS4 Bulk.app` |
| Windows (also from a Mac) | `wails build -platform windows/amd64` | `build/bin/sas4bulk.exe` |
| Linux (build on Linux) | `sudo apt install build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev`, then `wails build -tags webkit2_41` | `build/bin/sas4bulk` |

A plain `go build` is not enough for the window: Wails needs its build tags.
Use `wails build`, or `go build -tags desktop,production` on macOS/Windows.
`go test ./...` works as usual.

The apps are not code-signed:
- macOS: the first time, right-click the app → Open.
- Windows: SmartScreen may say "Windows protected your PC". Click More info → Run anyway.
- Windows needs the WebView2 runtime. It comes with Windows 10 and 11.

## Run

Double-click the app, or run it from a terminal to pass flags:

    "build/bin/SAS4 Bulk.app/Contents/MacOS/sas4bulk" --host other.server.ly
    sas4bulk.exe --web                 # in the browser instead of a window

| Flag | Meaning |
|---|---|
| `--host other.server.ly` | The system URL the login screen starts with (default `bss.libonet.ly`) |
| `--scheme http` | For a server without HTTPS |
| `--insecure` | Skip TLS certificate checks (self-signed certificates) |
| `--page-size 500` | Users per `index/user` request when loading |
| `--allow-unverified` | Let operations missing from the API docs run on more than one row |
| `--reports-dir DIR` | Where per-job CSV reports go. Default: `Documents/SAS4 Bulk/reports` in the window, `./reports` with `--web` |
| `--web` | Run as a local web server and open the browser (the old behaviour) |
| `--bind 0.0.0.0` | `--web` only: let other PCs on the network open it (prints a warning, locks the login URL) |
| `--port 9000` | `--web` only: another port if 8765 is busy |
| `--lock-url` / `--allow-url-change` | `--web` only: force the login URL locked / editable |
| `--no-browser` | `--web` only: don't open the browser |

The Windows `.exe` is a GUI program, so `--web` there prints nothing to the
console. Open `http://127.0.0.1:8765/` yourself.

## How the window works

`desktop.go` starts the app's HTTP server on a random `127.0.0.1` port. The
Wails window then loads it at `/?k=<secret>`. That secret is new on every
launch and is kept in a cookie. Any other request to that port gets 403, so
other programs on the machine cannot use the app. This keeps the login
cookie, file uploads and every API route the same as in the browser.

Webviews cannot download a file the way a browser does. In the window, the
UI sends `X-Save-Dialog: 1`, and the server shows a native Save dialog
(`Config.SaveFile`) instead of returning the file.

## Layout

    main.go                     flags, --web mode
    desktop.go                  the Wails window
    wails.json, build/          Wails project config and app icon (build/appicon.png)
    frontend/                   empty: Wails requires it; the UI lives in internal/app/static
    internal/sas4/              encrypted SAS4 API client
    internal/val/               Python-style coercion of loose JSON values
    internal/app/               directory, operations, jobs, bulk change,
                                reports, last sessions, HTTP routes
    internal/app/static/        the UI

`internal/app/static/app.js` started as a copy of `sas4_bulk/static/app.js`.
It now differs: all downloads go through one `download()` helper that knows
about the Save dialog. Copy any other UI changes across by hand.

## Tests

    go test ./...

`internal/app/fakesas4_test.go` is an in-memory SAS4 that speaks the real
protocol. `app_test.go` ports `tests/test_bulk_app.py` and also tests the
desktop guard and the Save-dialog downloads.

## Differences from the Python app

- The desktop window, and the Save dialog for downloads.
- Reports go to `--reports-dir`, not next to the package.
- The column picker lists the GUI's columns first, then extra fields A–Z.
- CSV delimiter sniffing (`,` `;` tab) is simpler than Python's `csv.Sniffer`.
# sastoolkit
