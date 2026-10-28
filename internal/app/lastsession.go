package app

// Last session per user, and one user's session history.
//
// index/UserSessions lists the sessions of ALL users (the admin GUI's
// Sessions report). Rather than one request per user, the listing is read
// newest-first and the first session seen for a username is that user's last
// one. The scan stops as soon as every user in the loaded list has been seen,
// or when it reaches sessions older than the look-back window -- so it costs
// a handful of pages, not one request per user.
//
// An empty acctstoptime means the session is still open (the user is
// online). RADIUS counts from the NAS side: acctinputoctets is what the user
// sent (upload), acctoutputoctets what they received (download).

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"sastoolkit/internal/sas4"
	"sastoolkit/internal/val"
)

const sessionRoute = "index/UserSessions"

// SessionColumns is exactly what the admin GUI's Sessions report asks for
// (captured from a live server).
var SessionColumns = []string{"username", "acctstarttime", "acctstoptime",
	"framedipaddress", "nasipaddress", "callingstationid", "acctinputoctets",
	"acctoutputoctets", "calledstationid", "nasportid", "acctterminatecause"}

var Lookbacks = []int{30, 90, 180, 365, 0} // days; 0 = the whole history

const (
	historyPage = 50
	maxReads    = 10 // SAS4 pages per history request
)

var sessionStates = []string{"online", "offline", "none"} // none = no session in the window

// SessionReportColumns is one row on screen and in the files. Raw bytes /
// seconds sit beside the readable ones so a spreadsheet can still sum them.
var SessionReportColumns = []Column{
	{"username", "Username"}, {"fullname", "Name"},
	{"profile_name", "Profile"}, {"parent_username", "Manager"},
	{"expiration", "Expiration"}, {"state", "Status"},
	{"start", "Session start"}, {"stop", "Session stop"},
	{"duration_h", "Duration"}, {"ip", "IP address"}, {"mac", "MAC"},
	{"nas_ip", "NAS IP"}, {"called_station", "Called station"},
	{"nas_port", "NAS port"}, {"upload_h", "Upload"},
	{"download_h", "Download"}, {"total_h", "Total"},
	{"terminate_cause", "Stop reason"}, {"upload", "Upload (bytes)"},
	{"download", "Download (bytes)"}, {"total", "Total (bytes)"},
	{"duration_s", "Duration (seconds)"},
}

// --------------------------------------------------------------------------
// FORMATTING
// --------------------------------------------------------------------------

func octets(v any) int64 {
	f, ok := val.Float(v)
	if !ok {
		return 0
	}
	return int64(f)
}

func humanBytes(n int64) string {
	size := float64(n)
	for _, unit := range []string{"B", "KB", "MB", "GB"} {
		if size < 1024 {
			if unit == "B" {
				return fmt.Sprintf("%.0f %s", size, unit)
			}
			return fmt.Sprintf("%.1f %s", size, unit)
		}
		size /= 1024
	}
	return fmt.Sprintf("%.2f TB", size)
}

func humanDuration(seconds any) string {
	s, ok := seconds.(int)
	if !ok {
		return ""
	}
	s = max(0, s)
	days, rest := s/86400, s%86400
	hours, rest := rest/3600, rest%3600
	minutes := rest / 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm %ds", minutes, s%60)
}

func strOr(v any) string {
	if !val.Truthy(v) {
		return ""
	}
	return val.Str(v)
}

func flattenSession(record M, now time.Time) M {
	start, stop := strOr(record["acctstarttime"]), strOr(record["acctstoptime"])
	began, hasBegan := parseDT(start)
	ended, hasEnded := parseDT(stop)
	online := stop == ""
	var duration any
	if hasBegan {
		end := began
		if online {
			end = now
		} else if hasEnded {
			end = ended
		}
		duration = int(end.Sub(began).Seconds())
	}
	upload := octets(record["acctinputoctets"])
	download := octets(record["acctoutputoctets"])
	state := "offline"
	if online {
		state = "online"
	}
	return M{
		"username": strings.TrimSpace(val.Str(record["username"])),
		"start":    start, "stop": stop,
		"online": online, "state": state,
		"ip":              strOr(record["framedipaddress"]),
		"nas_ip":          strOr(record["nasipaddress"]),
		"mac":             strOr(record["callingstationid"]),
		"called_station":  strOr(record["calledstationid"]),
		"nas_port":        strOr(record["nasportid"]),
		"terminate_cause": strOr(record["acctterminatecause"]),
		"upload":          upload, "download": download,
		"total":    upload + download,
		"upload_h": humanBytes(upload), "download_h": humanBytes(download),
		"total_h":    humanBytes(upload + download),
		"duration_s": duration, "duration_h": humanDuration(duration),
	}
}

// --------------------------------------------------------------------------
// LAST SESSION SCAN
// --------------------------------------------------------------------------

// LastSessions is the newest session of every user, for one login. Scanned
// in the background and kept until the next refresh.
type LastSessions struct {
	client   *sas4.Client
	PageSize int

	mu           sync.RWMutex
	byUsername   map[string]M // casefolded username -> session
	state        string       // empty | loading | ready | error
	err          any
	lookbackDays int
	progress     M
	stopped      any // why the scan ended
	scannedAt    any
	stopReasons  []string // distinct, for the filter's choices
}

func NewLastSessions(client *sas4.Client, pageSize int) *LastSessions {
	return &LastSessions{client: client, PageSize: pageSize,
		byUsername: map[string]M{}, state: "empty", lookbackDays: 90,
		progress: M{}}
}

func (l *LastSessions) State() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.state
}

func (l *LastSessions) ByUsername() map[string]M {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.byUsername
}

func (l *LastSessions) Status() M {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return M{"state": l.state, "error": l.err,
		"lookback_days": l.lookbackDays, "progress": val.Copy(l.progress),
		"stopped": l.stopped, "scanned_at": l.scannedAt,
		"stop_reasons": append([]string{}, l.stopReasons...)}
}

func (l *LastSessions) Refresh(usernames map[string]M, lookbackDays int) {
	l.mu.Lock()
	if l.state == "loading" {
		l.mu.Unlock()
		return
	}
	l.state, l.err = "loading", nil
	l.lookbackDays = lookbackDays
	l.progress = M{"pages": 0, "sessions": 0, "seen": 0,
		"users": len(usernames), "started": nowSeconds()}
	l.mu.Unlock()
	wanted := make(map[string]bool, len(usernames))
	for k := range usernames {
		wanted[k] = true
	}
	go l.scan(wanted)
}

var errStop = errors.New("stop")

func (l *LastSessions) scan(wanted map[string]bool) {
	now := time.Now()
	l.mu.RLock()
	lookback := l.lookbackDays
	l.mu.RUnlock()
	var cutoff time.Time
	if lookback > 0 {
		cutoff = now.AddDate(0, 0, -lookback)
	}
	found := map[string]M{}
	remaining := len(wanted)
	stopped := "end of the session history"
	sessions := 0
	update := func(kv ...any) {
		l.mu.Lock()
		for i := 0; i < len(kv); i += 2 {
			l.progress[kv[i].(string)] = kv[i+1]
		}
		l.mu.Unlock()
	}
	opt := sas4.DefaultPageOpts()
	opt.PageSize = l.PageSize
	opt.SortBy, opt.Direction = "acctstarttime", "desc"
	opt.OnPage = func(page int, last any, seen int, total any) {
		update("pages", page, "last_page", last, "total", total)
	}
	err := l.client.Paged(sessionRoute, SessionColumns, opt, func(rec M) bool {
		sessions++
		update("sessions", sessions)
		session := flattenSession(rec, now)
		began, ok := parseDT(session["start"])
		if !cutoff.IsZero() && ok && began.Before(cutoff) {
			stopped = fmt.Sprintf("reached sessions older than %d days", lookback)
			return false
		}
		key := val.Fold(val.Str(session["username"]))
		if key == "" || found[key] != nil {
			return true // an older session of a user seen already
		}
		found[key] = session
		if wanted[key] {
			remaining--
			update("seen", len(wanted)-remaining)
			if remaining == 0 {
				stopped = "every user found"
				return false
			}
		}
		return true
	})
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil { // shown in the UI, never fatal
		l.state, l.err = "error", err.Error()
		return
	}
	l.byUsername = found
	reasons := map[string]bool{}
	for _, session := range found {
		if r := val.Str(session["terminate_cause"]); r != "" {
			reasons[r] = true
		}
	}
	l.stopReasons = make([]string, 0, len(reasons))
	for r := range reasons {
		l.stopReasons = append(l.stopReasons, r)
	}
	sort.Strings(l.stopReasons)
	l.stopped = stopped
	l.scannedAt = now.Format("2006-01-02 15:04:05")
	l.state = "ready"
}

// SessionReportRows joins each user with their last session, newest session
// first; users without one in the window come last.
func SessionReportRows(users []M, last map[string]M, states []string) []M {
	wanted := map[string]bool{}
	for _, s := range states {
		for _, known := range sessionStates {
			if s == known {
				wanted[s] = true
			}
		}
	}
	rows := []M{}
	for _, user := range users {
		session := last[val.Fold(val.Str(user["username"]))]
		parts := []string{}
		for _, k := range []string{"firstname", "lastname"} {
			if val.Truthy(user[k]) {
				parts = append(parts, val.Str(user[k]))
			}
		}
		row := M{"user_id": user["user_id"], "username": user["username"],
			"fullname":        strings.Join(parts, " "),
			"profile_name":    strOr(user["profile_name"]),
			"parent_username": strOr(user["parent_username"]),
			"expiration":      strOr(user["expiration"])}
		if session != nil {
			for k, v := range session {
				if k != "username" {
					row[k] = v
				}
			}
		} else {
			row["state"], row["online"] = "none", false
		}
		if len(wanted) > 0 && !wanted[val.Str(row["state"])] {
			continue
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return val.Str(rows[i]["start"]) > val.Str(rows[j]["start"])
	})
	return rows
}

func SessionTotals(rows []M) M {
	counts := map[string]int{"online": 0, "offline": 0, "none": 0}
	for _, row := range rows {
		counts[val.Str(row["state"])]++
	}
	return M{"users": len(rows), "online": counts["online"],
		"offline": counts["offline"], "none": counts["none"]}
}

// --------------------------------------------------------------------------
// HISTORY
// --------------------------------------------------------------------------

// SessionHistory returns up to historyPage of a user's sessions, newest
// first, read from SAS4 page `cursor` on. SAS4's search is a substring match
// ('ali' also finds 'ali2'), so only exact username matches are kept, and
// further SAS4 pages are read (at most maxReads per call) until the page is
// full. `next` is the SAS4 page to continue from, or nil at the end.
func SessionHistory(c *sas4.Client, username string, cursor int) (M, error) {
	cursor = max(1, cursor)
	key := val.Fold(strings.TrimSpace(username))
	now := time.Now()
	rows := []M{}
	reads, next := 0, cursor
	for next != 0 && len(rows) < historyPage && reads < maxReads {
		status, data := c.ReadPost(sessionRoute, M{
			"page": next, "count": historyPage, "sortBy": "acctstarttime",
			"direction": "desc", "search": username, "columns": SessionColumns})
		obj, _ := data.(M)
		records, ok := obj["data"].([]any)
		if !ok {
			return nil, fmt.Errorf("cannot read sessions (HTTP %d)", status)
		}
		reads++
		for _, r := range records {
			rec, _ := r.(M)
			if val.Fold(strings.TrimSpace(val.Str(rec["username"]))) == key {
				rows = append(rows, flattenSession(rec, now))
			}
		}
		lastPage := next
		if n, ok := val.Int(obj["last_page"]); ok && n != 0 {
			lastPage = n
		}
		if len(records) > 0 && next < lastPage {
			next++
		} else {
			next = 0
		}
	}
	var nextOut any
	if next != 0 {
		nextOut = next
	}
	return M{"username": username, "cursor": cursor, "next": nextOut,
		"rows": rows}, nil
}
