# SAS toolkit

A desktop app for bulk actions over the SAS4 admin API. It opens in its own
window (built with [Wails](https://wails.io) v2). With `--web` it runs in the
browser instead. It is a single program with the UI built in, so the computer
it runs on needs nothing else installed.

What it does:

- **Users and managers:** loads the whole list once, then filters it by
  profile, manager, status, search text and expiry range.
- **Bulk operations:** add traffic, deposit and withdraw, activate, extend,
  change profile, cancel, create and rename from CSV, delete, enable and
  disable. For managers: deposit, withdraw, reward points and pay debt.
  - **Dry run** is on by default and sends nothing.
  - Every row is written to a CSV report as it finishes.
  - **Retry failed** re-sends only the failed rows.
- **Bulk change:** SAS4's own server-side Bulk Changes, applied in one
  request. The match count is checked again just before sending.
- **Reports:** active, new and expired users per day, week or month, and each
  user's last session. You can export them to CSV or XLSX.

## Quick start

1. Open `SAS toolkit.app` on macOS, or `sas-toolkit.exe` on Windows. Both
   are built into `build/bin/` (see Build below).
2. Sign in with a SAS4 manager account. The app has no login of its own.
3. Wait for the user list to load. It shows progress at the top.

The apps are not code-signed:

- macOS: the first time, right-click the app → Open.
- Windows: SmartScreen may say "Windows protected your PC". Click More info →
  Run anyway.
- Windows needs the WebView2 runtime. It comes with Windows 10 and 11.

## Build

You need:

- Go 1.27 or newer (see `go.mod`).
- On macOS, the Xcode command line tools.
- The Wails CLI, installed once:

      go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0
      wails doctor

`go install` puts `wails` in `~/go/bin`. If you get `command not found:
wails`, add that folder to your PATH:

    echo 'export PATH="$PATH:$HOME/go/bin"' >> ~/.zshrc && source ~/.zshrc

Then build from this folder:

| Platform | Command | Output |
|---|---|---|
| macOS (Apple Silicon and Intel) | `wails build -platform darwin/universal` | `build/bin/SAS toolkit.app` |
| Windows (also from a Mac) | `wails build -platform windows/amd64` | `build/bin/sas-toolkit.exe` |
| Linux (build on Linux) | `sudo apt install build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev`, then `wails build -tags webkit2_41` | `build/bin/sas-toolkit` |

## Develop

    wails dev                                   # the window, rebuilt when you save
    wails dev -appargs "--host other.server.ly" # the same, with app flags
    go run . --host other.server.ly             # the browser: quick, no Wails needed
    go test ./...

Notes:

- With `-appargs`, `wails dev` prints `flag provided but not defined: -host`
  and its own flag list. That warning is harmless: the app still gets the
  flags.
- `wails dev` also serves `http://localhost:34115`. You don't need it; the
  window is the app.
- The window only exists with Wails' build tags. `wails build` and
  `wails dev` set them for you. Without the tags, a plain `go run .` or
  `go build` opens the app in the browser instead (`desktop_none.go`).
- Run the package (`go run .`), not one file: `go run main.go` cannot see
  `desktop.go`.

## Flags

Run the app from a terminal to pass flags:

    "build/bin/SAS toolkit.app/Contents/MacOS/sas-toolkit" --host other.server.ly
    sas-toolkit.exe --web

| Flag | Meaning |
|---|---|
| `--host other.server.ly` | The system URL the login screen starts with (default `bss.libonet.ly`) |
| `--scheme http` | For a server without HTTPS |
| `--insecure` | Skip TLS certificate checks (self-signed certificates) |
| `--page-size 500` | Users per `index/user` request when loading |
| `--allow-unverified` | Let operations missing from the API docs run on more than one row |
| `--reports-dir DIR` | Where per-job CSV reports go. Default: `Documents/SAS toolkit/reports` in the window, `./reports` with `--web` |
| `--web` | Run as a local web server and open the browser instead of a window |
| `--bind 0.0.0.0` | `--web` only: let other PCs on the network open it (prints a warning, locks the login URL) |
| `--port 9000` | `--web` only: another port if 8765 is busy |
| `--lock-url` / `--allow-url-change` | `--web` only: force the login URL locked / editable |
| `--no-browser` | `--web` only: don't open the browser |

The Windows `.exe` is a GUI program, so with `--web` it prints nothing to the
console. Open `http://127.0.0.1:8765/` yourself.

## How the window works

`desktop.go` starts the app's HTTP server on a random `127.0.0.1` port. The
window first loads `/?k=<secret>`, with a secret that is new on every
launch. That page stores the secret in a cookie and moves itself on to `/`
with JavaScript. A server redirect would not work here: the window arrives
from Wails' own scheme, which counts as another site, and the cookie
(`SameSite=Strict`) would be dropped on the redirect. Any request without
the cookie gets 403, so other programs on the machine cannot use the app.

The UI is the same as in the browser: login cookie, file uploads and every
API route.

Webviews cannot download a file the way a browser does. In the window, the
UI sends `X-Save-Dialog: 1`, and the server shows a native Save dialog
(`Config.SaveFile`) instead of returning the file.

## Layout

    main.go                     flags, --web mode
    desktop.go                  the Wails window (desktop/dev/bindings build tags)
    desktop_none.go             without those tags: falls back to the browser
    VERSION, CHANGELOG.md       the current version and the change log
    docs/                       API docs: the app's API and the SAS4 API
    wails.json, build/          Wails project config and app icon (build/appicon.png);
                                build/bin/ holds the built apps
    frontend/                   empty apart from files Wails generates; the UI
                                lives in internal/app/static
    internal/sas4/              encrypted SAS4 API client
    internal/val/               coercion of loose JSON values
    internal/app/               directory, operations, jobs, bulk change,
                                reports, last sessions, HTTP routes
    internal/app/static/        the UI

In `internal/app/static/app.js`, all downloads go through one `download()`
helper that knows about the Save dialog.

## API docs

- [docs/API.md](docs/API.md): the app's own HTTP API (`/api/*`), and the
  operations.
- [docs/SAS4_API.md](docs/SAS4_API.md): the SAS4 API. It covers every request
  in the developer collection, says which ones the app uses, and lists the
  routes the app calls that the collection does not describe.

## Versions

The current version is in `VERSION`, and what changed in each one is in
[CHANGELOG.md](CHANGELOG.md). When you release, update both, and set the same
number in `productVersion` in `wails.json`.

Pushing a tag builds the release on GitHub
(`.github/workflows/release.yml`):

    git tag v1.0.0 && git push origin v1.0.0

The workflow checks that the tag matches `VERSION` and `wails.json`, runs the
tests, and builds for macOS (universal), Windows (amd64) and Linux (amd64).
It then publishes a release with the three builds, a `SHA256SUMS.txt`, and
that version's section of `CHANGELOG.md` as the release notes.

## Tests

    go test ./...

`internal/app/fakesas4_test.go` is an in-memory SAS4 that speaks the real
protocol. `app_test.go` tests the operations against it, plus the desktop
guard and the Save-dialog downloads.
