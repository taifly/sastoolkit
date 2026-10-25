//go:build desktop || dev || bindings

package main

// The desktop window. The UI is the same web app, served by app.New on a
// random 127.0.0.1 port; the window opens a loader page that moves to it.
// A real http origin (rather than Wails' own asset scheme) keeps the login
// cookie, fetch and file uploads working exactly as in a browser.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"sastoolkit/internal/app"
)

func runDesktop(s settings) {
	app.ReportsDir = desktopReportsDir(s)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	secret := make([]byte, 24)
	rand.Read(secret)
	key := hex.EncodeToString(secret)
	start := fmt.Sprintf("http://%s/?k=%s", listener.Addr(), key)

	var mu sync.Mutex
	var windowCtx context.Context
	cfg := s.config()
	cfg.AllowURLChange = true // only this window can reach the server
	cfg.Desktop = true
	cfg.DesktopSecret = key
	cfg.SaveFile = func(name string, data []byte) (string, error) {
		mu.Lock()
		ctx := windowCtx
		mu.Unlock()
		path, err := wailsruntime.SaveFileDialog(ctx, wailsruntime.SaveDialogOptions{
			Title: "Save " + name, DefaultFilename: name,
			CanCreateDirectories: true})
		if err != nil || path == "" {
			return "", err
		}
		return path, os.WriteFile(path, data, 0o644)
	}
	go http.Serve(listener, app.New(cfg))

	loader := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>SAS toolkit</title>`+
			`<script>location.replace(%q)</script>`, start)
	})

	err = wails.Run(&options.App{
		Title:       "SAS toolkit",
		Width:       1400,
		Height:      900,
		MinWidth:    1000,
		MinHeight:   650,
		AssetServer: &assetserver.Options{Handler: loader},
		OnStartup: func(ctx context.Context) {
			mu.Lock()
			windowCtx = ctx
			mu.Unlock()
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}
