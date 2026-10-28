// SAS toolkit: bulk SAS4 operations, in its own window.
//
//	sas-toolkit                                  # the login screen asks for the server
//	sas-toolkit --host demo4.sasradius.com --scheme http
//	sas-toolkit --web                            # in the browser, as a local server
//	sas-toolkit --web --bind 0.0.0.0 --port 8765 # reachable from the LAN
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"sastoolkit/internal/app"
)

type settings struct {
	host, scheme, bind, reportsDir string
	insecure, allowUnverified      bool
	lockURL, allowURLChange        bool
	noBrowser, web                 bool
	port, pageSize                 int
}

func main() {
	var s settings
	flag.BoolVar(&s.web, "web", false, "run as a local web server and open the browser instead of a window")
	flag.StringVar(&s.host, "host", "", "SAS4 server the login screen starts with")
	flag.StringVar(&s.scheme, "scheme", "https", "https or http")
	flag.BoolVar(&s.insecure, "insecure", false, "skip TLS certificate verification")
	flag.IntVar(&s.pageSize, "page-size", 500, "users per index/user request when loading")
	flag.BoolVar(&s.allowUnverified, "allow-unverified", false,
		"let operations missing from the API docs run on more than one row")
	flag.StringVar(&s.reportsDir, "reports-dir", "",
		"where job reports are written (default ./reports with --web, "+
			"Documents/SAS toolkit/reports in the window)")
	flag.StringVar(&s.bind, "bind", "127.0.0.1", "--web: address the app listens on")
	flag.IntVar(&s.port, "port", 8765, "--web: port the app listens on")
	flag.BoolVar(&s.lockURL, "lock-url", false,
		"--web: only allow signing in to --host (the default when --bind makes "+
			"the app reachable from the network)")
	flag.BoolVar(&s.allowURLChange, "allow-url-change", false,
		"--web: let the login screen sign in to another system even when "+
			"listening on the network")
	flag.BoolVar(&s.noBrowser, "no-browser", false, "--web: don't open the browser")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(),
			"SAS toolkit: bulk SAS4 operations.\n\nUsage of %s:\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if s.scheme != "https" && s.scheme != "http" {
		log.Fatalf("--scheme must be https or http, not %q", s.scheme)
	}
	if s.lockURL && s.allowURLChange {
		log.Fatal("--lock-url and --allow-url-change cannot be used together")
	}
	if s.web {
		runWeb(s)
	} else {
		runDesktop(s)
	}
}

func (s settings) config() app.Config {
	base := ""
	if s.host != "" {
		base = s.scheme + "://" + s.host
	}
	return app.Config{
		BaseURL:         base,
		Insecure:        s.insecure,
		AllowUnverified: s.allowUnverified,
		PageSize:        s.pageSize,
	}
}

func runWeb(s settings) {
	app.ReportsDir = s.reportsDir
	if app.ReportsDir == "" {
		app.ReportsDir = "reports"
	}
	local := s.bind == "127.0.0.1" || s.bind == "localhost" || s.bind == "::1"
	cfg := s.config()
	cfg.AllowURLChange = !s.lockURL && (local || s.allowURLChange)
	if !cfg.AllowURLChange && s.host == "" {
		log.Fatal("--host is required when the login screen is locked to one " +
			"system (--lock-url, or --bind without --allow-url-change)")
	}
	handler := app.New(cfg)

	shown := s.bind
	if shown == "0.0.0.0" || shown == "::" {
		shown = "127.0.0.1"
	}
	url := "http://" + net.JoinHostPort(shown, strconv.Itoa(s.port)) + "/"
	if !local {
		locked := ""
		if !cfg.AllowURLChange {
			locked = fmt.Sprintf(" The login screen is locked to %s.", s.host)
		}
		fmt.Printf("WARNING: listening on %s. Anyone who can reach this port "+
			"can log in with a SAS4 account and run bulk actions; traffic to "+
			"the app is plain HTTP.%s\n", s.bind, locked)
	}
	if s.host != "" {
		fmt.Printf("SAS toolkit for %s://%s -> %s\n", s.scheme, s.host, url)
	} else {
		fmt.Printf("SAS toolkit -> %s\n", url)
	}

	listener, err := net.Listen("tcp", net.JoinHostPort(s.bind, strconv.Itoa(s.port)))
	if err != nil {
		log.Fatal(err)
	}
	if !s.noBrowser {
		time.AfterFunc(time.Second, func() { openBrowser(url) })
	}
	log.Fatal(http.Serve(listener, handler))
}

// desktopReportsDir: a double-clicked app runs from "/", so reports go to
// the person's Documents folder.
func desktopReportsDir(s settings) string {
	if s.reportsDir != "" {
		return s.reportsDir
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "Documents", "SAS toolkit", "reports")
	}
	return "reports"
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}
