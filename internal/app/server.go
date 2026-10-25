package app

// HTTP routes for the bulk app.
//
// Logging in to the app IS logging in to SAS4: each browser session gets its
// own SAS4 client, directory and jobs, and can do exactly what that manager
// account can do in the admin GUI. There is no second set of credentials to
// manage.

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"crypto/rand"
	"encoding/base64"

	"sastoolkit/internal/sas4"
	"sastoolkit/internal/val"
)

//go:embed static
var staticFiles embed.FS

const (
	cookieName = "sastoolkit"
	maxImports = 20
	xlsxType   = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
)

type importData struct {
	op        string
	name      string
	rows      []M
	unmatched []M
}

type Session struct {
	token        string
	client       *sas4.Client
	directory    *Directory
	profiles     []M
	sites        []M
	lastSessions *LastSessions
	mu           sync.Mutex
	imports      map[int]*importData
	importID     int
}

// lookups builds the name -> id tables the payload builders resolve against.
func (s *Session) lookups() Ctx {
	table := func(items []M, idKey, nameKey string) Lookup {
		t := Lookup{ByID: map[int]string{}, ByName: map[string]int{}}
		for _, i := range items {
			id, ok := val.Int(i[idKey])
			if i[idKey] != nil && ok {
				t.ByID[id] = val.Str(i[nameKey])
			}
			if val.Truthy(i[nameKey]) && ok {
				t.ByName[val.Fold(strings.TrimSpace(val.Str(i[nameKey])))] = id
			}
		}
		return t
	}
	return Ctx{
		"profiles": table(s.profiles, "id", "name"),
		"sites":    table(s.sites, "id", "name"),
		"managers": table(s.directory.Managers(), "manager_id", "manager_username"),
	}
}

// --------------------------------------------------------------------------
// BODIES
// --------------------------------------------------------------------------

type loginBody struct {
	Username *string `json:"username"`
	Password *string `json:"password"`
	URL      *string `json:"url"` // the SAS4 system; default: --host
}

// NormaliseURL reduces what someone types or pastes to
// 'https://host[:port]'.
//
// 'bss.libonet.ly', 'https://bss.libonet.ly/#/login' and
// 'http://10.0.0.5:8080/admin/' all reduce to scheme + host, since the API
// path is added by the client. No scheme means https.
func NormaliseURL(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", errors.New("enter the system URL")
	}
	if !strings.Contains(text, "://") {
		text = "https://" + text
	}
	bad := fmt.Errorf("not a web address: '%s'", raw)
	u, err := url.Parse(text)
	if err != nil {
		return "", bad
	}
	scheme := strings.ToLower(u.Scheme)
	host := u.Hostname()
	if (scheme != "http" && scheme != "https") || host == "" || u.User != nil ||
		strings.ContainsAny(u.Host, " \t\r\n") {
		return "", bad
	}
	port := 0
	if p := u.Port(); p != "" {
		port, err = strconv.Atoi(p)
		if err != nil || port < 0 || port > 65535 {
			return "", bad
		}
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]" // IPv6 literal
	}
	out := scheme + "://" + strings.ToLower(host)
	if port != 0 {
		out += ":" + strconv.Itoa(port)
	}
	return out, nil
}

type userQuery struct {
	Filters  UserFilters `json:"filters"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
}

type reportBody struct {
	Period   string   `json:"period"`
	DateFrom *string  `json:"date_from"`
	DateTo   *string  `json:"date_to"`
	Profiles IntList  `json:"profiles"`
	Managers []string `json:"managers"`
	Format   *string  `json:"format"` // csv | xlsx for a download
}

type sessionScanBody struct {
	LookbackDays int `json:"lookback_days"` // 0 = the whole history
}

type sessionQuery struct {
	Filters  UserFilters `json:"filters"`
	States   []string    `json:"states"` // online | offline | none
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
	Format   *string     `json:"format"`
}

type historyBody struct {
	Username *string `json:"username"`
	Cursor   int     `json:"cursor"` // the SAS4 page to read from
}

type exportBody struct {
	Filters UserFilters `json:"filters"`
	IDs     *IntList    `json:"ids"`     // ticked rows; nil = all matching
	Columns []Column    `json:"columns"` // empty = every field
	Format  string      `json:"format"`  // csv | xlsx
}

type managerQuery struct {
	Search   string `json:"search"`
	Page     int    `json:"page"`
	PageSize int    `json:"page_size"`
}

type source struct {
	Type     string      `json:"type"` // filter | managers | import
	Filters  UserFilters `json:"filters"`
	Search   string      `json:"search"`
	IDs      *IntList    `json:"ids"` // ticked rows; nil = all matching
	ImportID *int        `json:"import_id"`
}

type bulkPreviewBody struct {
	Filters []BulkFilterRow `json:"filters"`
}

type bulkApplyBody struct {
	Filters       []BulkFilterRow `json:"filters"`
	Action        *string         `json:"action"`
	Option        any             `json:"option"`
	ExpectedCount *int            `json:"expected_count"`
}

type jobBody struct {
	Op      *string `json:"op"`
	Params  M       `json:"params"`
	DryRun  bool    `json:"dry_run"`
	Workers int     `json:"workers"`
	Source  *source `json:"source"`
}

// --------------------------------------------------------------------------
// PLUMBING
// --------------------------------------------------------------------------

// httpError becomes {"detail": msg} with its status, like FastAPI's
// HTTPException, so the UI's error handling works unchanged.
type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }

func fail(code int, format string, args ...any) error {
	return &httpError{code, fmt.Sprintf(format, args...)}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		code = 500
		buf.Reset()
		buf.WriteString(`{"detail":"cannot encode the response"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(buf.Bytes())
}

func jsonUnmarshal(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return dec.Decode(v)
}

// decode reads a JSON body into v, which holds the defaults already.
func decode(r *http.Request, v any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		return fail(400, "cannot read the request")
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return fail(422, "a JSON body is required")
	}
	if err := jsonUnmarshal(raw, v); err != nil {
		return fail(422, "bad request body: %v", err)
	}
	return nil
}

type handler func(w http.ResponseWriter, r *http.Request) error

func (h handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if p := recover(); p != nil {
			writeJSON(w, 500, M{"detail": fmt.Sprintf("internal error: %v", p)})
		}
	}()
	if err := h(w, r); err != nil {
		var he *httpError
		if errors.As(err, &he) {
			writeJSON(w, he.code, M{"detail": he.msg})
			return
		}
		writeJSON(w, 500, M{"detail": err.Error()})
	}
}

func ok(w http.ResponseWriter, v any) error {
	writeJSON(w, 200, v)
	return nil
}

// wantsSaveDialog: the desktop window asked for the file to be saved through
// a native Save dialog, since its webview cannot download a blob.
func (a *App) wantsSaveDialog(r *http.Request) bool {
	return a.cfg.SaveFile != nil && r.Header.Get("X-Save-Dialog") == "1"
}

// deliver sends a file as a download, or -- for the desktop window -- saves
// it where the person picks and answers {"saved": path} / {"cancelled": true}.
func (a *App) deliver(w http.ResponseWriter, r *http.Request, name, contentType string, body []byte) error {
	if a.wantsSaveDialog(r) {
		path, err := a.cfg.SaveFile(name, body)
		if err != nil {
			return fail(500, "cannot save %s: %v", name, err)
		}
		if path == "" {
			return ok(w, M{"cancelled": true})
		}
		return ok(w, M{"saved": path})
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.WriteHeader(200)
	w.Write(body)
	return nil
}

// deliverCSV streams a CSV export, or buffers it for the Save dialog.
func (a *App) deliverCSV(w http.ResponseWriter, r *http.Request, name string, rows []M, columns []Column) error {
	if a.wantsSaveDialog(r) {
		var buf bytes.Buffer
		if err := ExportCSV(&buf, rows, columns); err != nil {
			return err
		}
		return a.deliver(w, r, name, "text/csv; charset=utf-8", buf.Bytes())
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	ExportCSV(w, rows, columns)
	return nil
}

func pageOf(rows []M, page, size int) M {
	size = max(1, min(size, 500))
	page = max(1, page)
	from := min(len(rows), (page-1)*size)
	to := min(len(rows), page*size)
	return M{"total": len(rows), "page": page, "page_size": size,
		"rows": rows[from:to]}
}

func newToken() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func formatOf(f *string) string {
	if f == nil {
		return ""
	}
	return *f
}

func filterIDs(rows []M, key string, ids *IntList) []M {
	if ids == nil {
		return rows
	}
	wanted := map[int]bool{}
	for _, id := range *ids {
		wanted[id] = true
	}
	out := []M{}
	for _, r := range rows {
		if n, ok := val.Int(r[key]); ok && wanted[n] {
			out = append(out, r)
		}
	}
	return out
}

func public(row M) M {
	out := M{}
	for k, v := range row {
		if k != "_csv" {
			out[k] = v
		}
	}
	for k, v := range csvOf(row) {
		if _, has := out[k]; !has {
			out[k] = v
		}
	}
	if val.Truthy(out["password"]) {
		out["password"] = "***"
	}
	return out
}

func profileCounts(rows []M) [][]any {
	counts := map[string]int{}
	order := []string{}
	for _, r := range rows {
		key := val.Str(r["profile_name"])
		if !val.Truthy(r["profile_name"]) {
			key = "#" + val.Str(r["profile_id"])
		}
		if _, seen := counts[key]; !seen {
			order = append(order, key)
		}
		counts[key]++
	}
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
	out := [][]any{}
	for i, k := range order {
		if i == 20 {
			break
		}
		out = append(out, []any{k, counts[k]})
	}
	return out
}

func opByKey(key string) (*Operation, error) {
	if op, ok := ByKey[key]; ok {
		return op, nil
	}
	return nil, fail(404, "unknown operation %s", key)
}

// --------------------------------------------------------------------------
// APP
// --------------------------------------------------------------------------

type Config struct {
	BaseURL         string // the system the login screen offers
	Insecure        bool
	AllowUnverified bool
	PageSize        int
	// AllowURLChange lets the person sign in to another system; each session
	// then talks to its own. Turned off when the app is reachable from the
	// network, so it cannot be used to make this machine request arbitrary
	// addresses.
	AllowURLChange bool
	// ClientOptions are used for every SAS4 client (tests shorten timeouts).
	ClientOptions sas4.Options

	// Desktop: the app runs inside the desktop window. Every request must
	// carry the cookie set from DesktopSecret (the window opens /?k=secret),
	// so other programs on this machine cannot use the loopback port.
	Desktop       bool
	DesktopSecret string
	// SaveFile saves a download where the person picks (a native Save
	// dialog); "" means they cancelled. Only set in the desktop window.
	SaveFile func(name string, data []byte) (string, error)
}

type App struct {
	cfg      Config
	mu       sync.Mutex
	sessions map[string]*Session
	board    *JobBoard
}

func New(cfg Config) http.Handler {
	if cfg.PageSize == 0 {
		cfg.PageSize = 500
	}
	cfg.ClientOptions.Insecure = cfg.ClientOptions.Insecure || cfg.Insecure
	a := &App{cfg: cfg, sessions: map[string]*Session{}, board: NewJobBoard()}
	if cfg.Desktop {
		return a.desktopGuard(a.routes())
	}
	return a.routes()
}

const desktopCookie = "sastoolkit_window"

// desktopGuard lets in only the desktop window: it arrives once with
// ?k=<secret>, gets an HttpOnly cookie, and must send it from then on.
func (a *App) desktopGuard(next http.Handler) http.Handler {
	secret := a.cfg.DesktopSecret
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if k := r.URL.Query().Get("k"); k != "" && k == secret {
			http.SetCookie(w, &http.Cookie{Name: desktopCookie, Value: secret,
				Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			// Not a 302: the window arrives here from Wails' own scheme, a
			// cross-site navigation, and a redirect that continues it would
			// not carry the SameSite=Strict cookie. A same-origin page that
			// moves itself on does.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>SAS toolkit</title>`+
				`<script>location.replace("/")</script>`)
			return
		}
		if c, err := r.Cookie(desktopCookie); err != nil || secret == "" || c.Value != secret {
			writeJSON(w, 403, M{"detail": "open this app from its window"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) session(r *http.Request) *Session {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[c.Value]
}

func (a *App) current(r *http.Request) (*Session, error) {
	if s := a.session(r); s != nil {
		return s, nil
	}
	return nil, fail(401, "not logged in")
}

func ready(s *Session) error {
	if s.directory.State() != "ready" {
		return fail(409, "the user list is still loading")
	}
	return nil
}

func pathInt(r *http.Request, name string) (int, error) {
	n, err := strconv.Atoi(r.PathValue(name))
	if err != nil {
		return 0, fail(422, "%s must be an integer", name)
	}
	return n, nil
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	handle := func(pattern string, h handler) { mux.Handle(pattern, h) }

	// -- session ------------------------------------------------------------

	handle("POST /api/login", a.login)
	handle("POST /api/logout", func(w http.ResponseWriter, r *http.Request) error {
		if c, err := r.Cookie(cookieName); err == nil {
			a.mu.Lock()
			delete(a.sessions, c.Value)
			a.mu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/",
			MaxAge: -1, Expires: time.Unix(0, 0)})
		return ok(w, M{"ok": true})
	})
	handle("GET /api/session", func(w http.ResponseWriter, r *http.Request) error {
		s := a.session(r)
		out := M{"logged_in": s != nil, "host": a.cfg.BaseURL,
			"default_host": a.cfg.BaseURL, "allow_url_change": a.cfg.AllowURLChange,
			"username": nil, "allow_unverified": a.cfg.AllowUnverified,
			"desktop": a.cfg.Desktop}
		if s != nil {
			out["host"], out["username"] = s.client.Origin, s.client.Username
		}
		return ok(w, out)
	})

	// -- reference data -----------------------------------------------------

	handle("GET /api/meta", func(w http.ResponseWriter, r *http.Request) error {
		s, err := a.current(r)
		if err != nil {
			return err
		}
		managers := []M{}
		for _, m := range s.directory.Managers() {
			managers = append(managers, M{"id": m["manager_id"], "name": m["manager_username"]})
		}
		ops := []M{}
		for _, op := range Operations {
			ops = append(ops, op.Describe())
		}
		return ok(w, M{"host": s.client.Origin, "username": s.client.Username,
			"allow_unverified": a.cfg.AllowUnverified,
			"profiles":         s.profiles, "sites": s.sites, "managers": managers,
			"operations": ops, "bulk": BulkDescribe()})
	})
	handle("GET /api/directory", func(w http.ResponseWriter, r *http.Request) error {
		s, err := a.current(r)
		if err != nil {
			return err
		}
		return ok(w, s.directory.Status())
	})
	handle("POST /api/directory/refresh", func(w http.ResponseWriter, r *http.Request) error {
		s, err := a.current(r)
		if err != nil {
			return err
		}
		s.directory.Refresh()
		return ok(w, s.directory.Status())
	})
	handle("GET /api/extensions/{profile_id}", func(w http.ResponseWriter, r *http.Request) error {
		id, err := pathInt(r, "profile_id")
		if err != nil {
			return err
		}
		s, err := a.current(r)
		if err != nil {
			return err
		}
		return ok(w, M{"extensions": s.client.AllowedExtensions(id)})
	})

	// -- selection ----------------------------------------------------------

	handle("POST /api/users/query", a.usersQuery)
	handle("POST /api/users/export", a.usersExport)
	handle("POST /api/reports", a.report)

	// -- last session report ------------------------------------------------

	handle("POST /api/sessions/refresh", a.sessionsRefresh)
	handle("GET /api/sessions/status", func(w http.ResponseWriter, r *http.Request) error {
		s, err := a.current(r)
		if err != nil {
			return err
		}
		return ok(w, s.lastSessions.Status())
	})
	handle("POST /api/sessions/query", a.sessionsQuery)
	handle("POST /api/sessions/history", a.sessionsHistory)
	handle("POST /api/managers/query", func(w http.ResponseWriter, r *http.Request) error {
		s, err := a.current(r)
		if err != nil {
			return err
		}
		body := managerQuery{Page: 1, PageSize: 50}
		if err := decode(r, &body); err != nil {
			return err
		}
		if err := ready(s); err != nil {
			return err
		}
		return ok(w, pageOf(s.directory.FilterManagers(body.Search), body.Page, body.PageSize))
	})
	handle("POST /api/import", a.importCSV)

	// -- bulk change and jobs -----------------------------------------------

	handle("POST /api/bulk/preview", a.bulkPreview)
	handle("POST /api/bulk/apply", a.bulkApply)
	handle("POST /api/jobs", a.createJob)
	handle("GET /api/jobs", func(w http.ResponseWriter, r *http.Request) error {
		s, err := a.current(r)
		if err != nil {
			return err
		}
		jobs := []M{}
		for _, j := range a.board.Mine(s.token) {
			jobs = append(jobs, j.Summary(0))
		}
		return ok(w, M{"jobs": jobs})
	})
	handle("GET /api/jobs/{job_id}", func(w http.ResponseWriter, r *http.Request) error {
		_, job, err := a.owned(r)
		if err != nil {
			return err
		}
		return ok(w, job.Summary(30))
	})
	handle("POST /api/jobs/{job_id}/cancel", func(w http.ResponseWriter, r *http.Request) error {
		_, job, err := a.owned(r)
		if err != nil {
			return err
		}
		job.Cancel()
		return ok(w, job.Summary(0))
	})
	handle("POST /api/jobs/{job_id}/retry-failed", a.retryFailed)
	handle("GET /api/jobs/{job_id}/results.csv", func(w http.ResponseWriter, r *http.Request) error {
		_, job, err := a.owned(r)
		if err != nil {
			return err
		}
		name := filepath.Base(job.ReportPath())
		if a.wantsSaveDialog(r) {
			data, err := os.ReadFile(job.ReportPath())
			if err != nil {
				return fail(404, "the report is not written yet")
			}
			return a.deliver(w, r, name, "text/csv; charset=utf-8", data)
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
		http.ServeFile(w, r, job.ReportPath())
		return nil
	})

	// -- page ---------------------------------------------------------------

	static, _ := fs.Sub(staticFiles, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		page, _ := fs.ReadFile(static, "index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})
	return mux
}

func (a *App) login(w http.ResponseWriter, r *http.Request) error {
	var body loginBody
	if err := decode(r, &body); err != nil {
		return err
	}
	if body.Username == nil || body.Password == nil {
		return fail(422, "username and password are required")
	}
	target := a.cfg.BaseURL
	if body.URL != nil && strings.TrimSpace(*body.URL) != "" {
		u, err := NormaliseURL(*body.URL)
		if err != nil {
			return fail(400, "%s", err.Error())
		}
		base, _ := NormaliseURL(a.cfg.BaseURL)
		if u != base && !a.cfg.AllowURLChange {
			return fail(403, "this app is locked to %s", a.cfg.BaseURL)
		}
		target = u
	}
	client := sas4.New(target, *body.Username, *body.Password, a.cfg.ClientOptions)
	if err := client.Login(); err != nil {
		return fail(401, "%s", err.Error())
	}
	token := newToken()
	s := &Session{token: token, client: client,
		directory:    NewDirectory(client, a.cfg.PageSize),
		lastSessions: NewLastSessions(client, a.cfg.PageSize),
		imports:      map[int]*importData{}}
	profiles, err := client.Profiles()
	if err != nil {
		return fail(502, "logged in, but profiles failed: %v", err)
	}
	s.profiles = profiles
	s.sites = client.Sites() // optional: only Create uses it
	s.directory.Refresh()
	a.mu.Lock()
	a.sessions[token] = s
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode})
	return ok(w, M{"ok": true, "username": *body.Username, "host": target})
}

func (a *App) usersQuery(w http.ResponseWriter, r *http.Request) error {
	// Browsing works mid-load over the users fetched so far; running a job
	// does not (resolveRows calls ready()).
	s, err := a.current(r)
	if err != nil {
		return err
	}
	body := userQuery{Page: 1, PageSize: 50}
	if err := decode(r, &body); err != nil {
		return err
	}
	d := s.directory
	state := d.State()
	if state != "ready" && len(d.CurrentUsers()) == 0 {
		return fail(409, "the user list is still loading")
	}
	rows := d.FilterUsers(body.Filters)
	out := pageOf(rows, body.Page, body.PageSize)
	out["partial"] = state != "ready"
	out["profiles"] = profileCounts(rows)
	return ok(w, out)
}

func (a *App) usersExport(w http.ResponseWriter, r *http.Request) error {
	s, err := a.current(r)
	if err != nil {
		return err
	}
	body := exportBody{Format: "csv"}
	if err := decode(r, &body); err != nil {
		return err
	}
	if err := ready(s); err != nil { // never export half a list
		return err
	}
	users := filterIDs(s.directory.FilterUsers(body.Filters), "user_id", body.IDs)
	if len(users) == 0 {
		return fail(400, "no users to export")
	}
	columns := body.Columns
	if len(columns) == 0 {
		for _, k := range s.directory.Fields() {
			columns = append(columns, Column{k, k})
		}
	}
	name := fmt.Sprintf("sas4_users_%d_%s", len(users), time.Now().Format("2006-01-02_15-04-05"))
	w.Header().Set("X-Row-Count", strconv.Itoa(len(users)))
	switch body.Format {
	case "xlsx":
		blob, err := ExportXLSX(users, columns)
		if err != nil {
			return err
		}
		return a.deliver(w, r, name+".xlsx", xlsxType, blob)
	case "csv":
		return a.deliverCSV(w, r, name+".csv", users, columns)
	}
	w.Header().Del("X-Row-Count")
	return fail(400, "unknown format %s", body.Format)
}

func (a *App) report(w http.ResponseWriter, r *http.Request) error {
	s, err := a.current(r)
	if err != nil {
		return err
	}
	body := reportBody{Period: "day"}
	if err := decode(r, &body); err != nil {
		return err
	}
	if err := ready(s); err != nil { // a partial list would undercount
		return err
	}
	result, err := BuildReport(s.directory, body.Period, body.DateFrom, body.DateTo,
		body.Profiles, body.Managers, time.Now())
	if err != nil {
		return fail(400, "%s", err.Error())
	}
	format := formatOf(body.Format)
	if format == "" {
		return ok(w, result)
	}
	name := fmt.Sprintf("sas4_report_%s_%s_%s", body.Period, result["from"], result["to"])
	switch format {
	case "xlsx":
		blob, err := ReportXLSX(result)
		if err != nil {
			return err
		}
		return a.deliver(w, r, name+".xlsx", xlsxType, blob)
	case "csv":
		return a.deliver(w, r, name+".csv", "text/csv; charset=utf-8", ReportCSV(result))
	}
	return fail(400, "unknown format %s", format)
}

func (a *App) sessionsRefresh(w http.ResponseWriter, r *http.Request) error {
	s, err := a.current(r)
	if err != nil {
		return err
	}
	body := sessionScanBody{LookbackDays: 90}
	if err := decode(r, &body); err != nil {
		return err
	}
	if err := ready(s); err != nil { // the scan stops once all are seen
		return err
	}
	allowed := false
	names := []string{}
	for _, l := range Lookbacks {
		allowed = allowed || l == body.LookbackDays
		names = append(names, strconv.Itoa(l))
	}
	if !allowed {
		return fail(400, "look-back must be one of %s", strings.Join(names, ", "))
	}
	s.lastSessions.Refresh(s.directory.ByUsername(), body.LookbackDays)
	return ok(w, s.lastSessions.Status())
}

func (a *App) sessionsQuery(w http.ResponseWriter, r *http.Request) error {
	s, err := a.current(r)
	if err != nil {
		return err
	}
	body := sessionQuery{Page: 1, PageSize: 100}
	if err := decode(r, &body); err != nil {
		return err
	}
	if err := ready(s); err != nil {
		return err
	}
	last := s.lastSessions
	if last.State() != "ready" {
		return fail(409, "scan the sessions first")
	}
	rows := SessionReportRows(s.directory.FilterUsers(body.Filters),
		last.ByUsername(), body.States)
	format := formatOf(body.Format)
	if format == "" {
		out := pageOf(rows, body.Page, body.PageSize)
		for k, v := range last.Status() {
			out[k] = v
		}
		out["totals"] = SessionTotals(rows)
		return ok(w, out)
	}
	if len(rows) == 0 {
		return fail(400, "no users to export")
	}
	name := fmt.Sprintf("sas4_last_sessions_%d_%s", len(rows),
		time.Now().Format("2006-01-02_15-04-05"))
	switch format {
	case "xlsx":
		blob, err := ExportXLSX(rows, SessionReportColumns)
		if err != nil {
			return err
		}
		return a.deliver(w, r, name+".xlsx", xlsxType, blob)
	case "csv":
		return a.deliverCSV(w, r, name+".csv", rows, SessionReportColumns)
	}
	return fail(400, "unknown format %s", format)
}

func (a *App) sessionsHistory(w http.ResponseWriter, r *http.Request) error {
	s, err := a.current(r)
	if err != nil {
		return err
	}
	body := historyBody{Cursor: 1}
	if err := decode(r, &body); err != nil {
		return err
	}
	if body.Username == nil {
		return fail(422, "username is required")
	}
	username := strings.TrimSpace(*body.Username)
	if username == "" {
		return fail(400, "username is required")
	}
	result, err := SessionHistory(s.client, username, body.Cursor)
	if err != nil {
		return fail(502, "%s", err.Error())
	}
	user := s.directory.ByUsername()[val.Fold(username)]
	info := M{}
	for _, k := range []string{"user_id", "firstname", "lastname", "profile_name",
		"parent_username", "expiration", "phone"} {
		info[k] = user[k]
	}
	result["user"] = info
	return ok(w, result)
}

func (a *App) importCSV(w http.ResponseWriter, r *http.Request) error {
	s, err := a.current(r)
	if err != nil {
		return err
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return fail(422, "expected a multipart form with op and file")
	}
	opKey := r.FormValue("op")
	file, header, err := r.FormFile("file")
	if opKey == "" || err != nil {
		return fail(422, "expected a multipart form with op and file")
	}
	defer file.Close()
	if err := ready(s); err != nil {
		return err
	}
	op, err := opByKey(opKey)
	if err != nil {
		return err
	}
	blob, err := io.ReadAll(file)
	if err != nil {
		return fail(400, "cannot read the file")
	}
	rows := ReadCSVBytes(blob)
	if len(rows) == 0 {
		return fail(400, "the file has no rows")
	}
	matched, unmatched := s.directory.MatchImport(rows, op.Target)
	s.mu.Lock()
	s.importID++
	id := s.importID
	s.imports[id] = &importData{op: opKey, name: header.Filename,
		rows: matched, unmatched: unmatched}
	ids := []int{}
	for k := range s.imports {
		ids = append(ids, k)
	}
	sort.Ints(ids)
	for _, old := range ids[:max(0, len(ids)-maxImports)] {
		delete(s.imports, old)
	}
	s.mu.Unlock()

	columnSet := map[string]bool{}
	for _, row := range rows {
		for k := range row {
			columnSet[k] = true
		}
	}
	columns := []string{}
	for k := range columnSet {
		columns = append(columns, k)
	}
	sort.Strings(columns)
	preview := []M{}
	for _, m := range matched[:min(50, len(matched))] {
		preview = append(preview, public(m))
	}
	return ok(w, M{"import_id": id, "name": header.Filename,
		"columns": columns, "matched": len(matched),
		"unmatched_count": len(unmatched),
		"unmatched":       unmatched[:min(200, len(unmatched))],
		"preview":         preview})
}

// -- jobs -------------------------------------------------------------------

func (a *App) resolveRows(s *Session, op *Operation, src *source) ([]M, error) {
	if src.Type == "import" {
		s.mu.Lock()
		var imported *importData
		if src.ImportID != nil {
			imported = s.imports[*src.ImportID]
		}
		s.mu.Unlock()
		if imported == nil {
			return nil, fail(404, "that import has expired; upload the file again")
		}
		return append([]M{}, imported.rows...), nil
	}
	if err := ready(s); err != nil {
		return nil, err
	}
	if op.Target == "new_users" {
		return nil, fail(400, "'%s' takes a CSV import", op.Label)
	}
	var rows []M
	var key string
	if src.Type == "managers" || op.Target == "managers" {
		if op.Target != "managers" {
			return nil, fail(400, "this operation runs on users")
		}
		rows, key = s.directory.FilterManagers(src.Search), "manager_id"
	} else {
		if op.Target != "users" {
			return nil, fail(400, "this operation runs on managers")
		}
		rows, key = s.directory.FilterUsers(src.Filters), "user_id"
	}
	return filterIDs(rows, key, src.IDs), nil
}

func (a *App) bulkFilters(s *Session, rows []BulkFilterRow) ([]M, error) {
	filters, err := BuildFilter(rows, s.lookups())
	if err != nil {
		return nil, fail(400, "%s", err.Error())
	}
	return filters, nil
}

func (a *App) bulkPreview(w http.ResponseWriter, r *http.Request) error {
	s, err := a.current(r)
	if err != nil {
		return err
	}
	var body bulkPreviewBody
	if err := decode(r, &body); err != nil {
		return err
	}
	filters, err := a.bulkFilters(s, body.Filters)
	if err != nil {
		return err
	}
	result, err := BulkPreview(s.client, filters)
	if err != nil {
		return fail(502, "%s", err.Error())
	}
	result["filter"] = filters
	return ok(w, result)
}

func (a *App) bulkApply(w http.ResponseWriter, r *http.Request) error {
	s, err := a.current(r)
	if err != nil {
		return err
	}
	var body bulkApplyBody
	if err := decode(r, &body); err != nil {
		return err
	}
	if body.Action == nil || body.ExpectedCount == nil {
		return fail(422, "action and expected_count are required")
	}
	filters, err := a.bulkFilters(s, body.Filters)
	if err != nil {
		return err
	}
	action, err := BuildAction(*body.Action, body.Option)
	if err != nil {
		return fail(400, "%s", err.Error())
	}
	if *body.ExpectedCount <= 0 {
		return fail(400, "preview first: nothing is matched")
	}
	job := NewBulkChangeJob(s.client, ByKey["bulk_change"], filters, action,
		*body.ExpectedCount, s.token, BulkSummarise(filters, action))
	a.board.Add(job)
	return ok(w, job.Summary(0))
}

func (a *App) createJob(w http.ResponseWriter, r *http.Request) error {
	s, err := a.current(r)
	if err != nil {
		return err
	}
	body := jobBody{DryRun: true, Workers: 4}
	if err := decode(r, &body); err != nil {
		return err
	}
	if body.Op == nil || body.Source == nil {
		return fail(422, "op and source are required")
	}
	if body.Params == nil {
		body.Params = M{}
	}
	op, err := opByKey(*body.Op)
	if err != nil {
		return err
	}
	if op.Target == "server_bulk" {
		return fail(400, "use /api/bulk/apply for a bulk change")
	}
	rows, err := a.resolveRows(s, op, body.Source)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return fail(400, "no rows selected")
	}
	missing := []string{}
	for _, f := range op.Fields {
		if !f.Required || !val.Blank(body.Params[f.Name]) {
			continue
		}
		everyRow := true
		for _, row := range rows {
			if csvOf(row)[f.Name] == "" {
				everyRow = false
				break
			}
		}
		if !everyRow {
			missing = append(missing, f.Label)
		}
	}
	if len(missing) > 0 {
		return fail(400, "fill in: %s", strings.Join(missing, ", "))
	}
	job, err := NewJob(s.client, op, rows, body.Params, s.lookups(), body.DryRun,
		body.Workers, s.token, a.cfg.AllowUnverified, nil)
	if err != nil {
		return fail(400, "%s", err.Error())
	}
	a.board.Add(job)
	return ok(w, job.Summary(0))
}

func (a *App) owned(r *http.Request) (*Session, Runner, error) {
	id, err := pathInt(r, "job_id")
	if err != nil {
		return nil, nil, err
	}
	s, err := a.current(r)
	if err != nil {
		return nil, nil, err
	}
	job := a.board.Get(id, s.token)
	if job == nil {
		return nil, nil, fail(404, "no such job")
	}
	return s, job, nil
}

func (a *App) retryFailed(w http.ResponseWriter, r *http.Request) error {
	s, runner, err := a.owned(r)
	if err != nil {
		return err
	}
	if runner.Running() {
		return fail(409, "wait for the job to finish")
	}
	if runner.IsDryRun() {
		return fail(400, "a dry run has nothing to retry")
	}
	rows := runner.FailedRows()
	job, isJob := runner.(*Job)
	if len(rows) == 0 || !isJob {
		return fail(400, "no failed rows")
	}
	retry, err := NewJob(s.client, job.Op, rows, job.Params, s.lookups(), false,
		job.Workers, s.token, true, job.ID())
	if err != nil {
		return fail(400, "%s", err.Error())
	}
	a.board.Add(retry)
	return ok(w, retry.Summary(0))
}
