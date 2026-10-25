//go:build !desktop && !dev && !bindings

package main

import "fmt"

// runDesktop without the window: a plain `go build` / `go run .` has no Wails
// build tags, so the app runs in the browser instead. `wails build` and
// `wails dev` set the `desktop` tag and get the window (desktop.go).
func runDesktop(s settings) {
	fmt.Println("Built without the desktop window (use `wails build` or " +
		"`wails dev` for it) -- opening in the browser instead.")
	runWeb(s)
}
