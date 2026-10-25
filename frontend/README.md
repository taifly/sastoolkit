Empty on purpose: the window loads the UI from internal/app/static through
the in-process server (see desktop.go). Wails only needs this folder to exist.
