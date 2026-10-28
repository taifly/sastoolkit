package app

// Who a bulk operation runs on: a cached copy of the user and manager lists,
// filters over it, and CSV imports matched against it.
//
// index/user documents page/count/search but no profile or status filter, so
// the whole listing is read once (about 360 requests at 500 a page) and
// filtered here. It stays in memory for the login session; Refresh re-reads
// it.

import (
	"bytes"
	"encoding/csv"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"sastoolkit/internal/sas4"
	"sastoolkit/internal/val"
)

type M = val.M

// UserColumns is exactly what the admin GUI's user list asks for (captured
// from a live server), so the server returns every column the GUI can show.
var UserColumns = []string{"n_row", "idx", "id", "username", "firstname",
	"lastname", "expiration", "parent_username", "name", "balance",
	"loan_balance", "group_name", "traffic", "city", "remaining_days",
	"static_ip", "notes", "last_online", "company", "simultaneous_sessions",
	"used_traffic", "phone", "address", "contract_id", "created_at",
	"available_traffic", "national_id", "mikrotik_ipv6_prefix", "site_name",
	"pin_tries", "debt_days"}

// Row numbers of the GUI's own table, meaningless here.
var droppedFields = map[string]bool{"n_row": true, "idx": true}

var ManagerColumns = []string{"id", "username", "firstname", "lastname",
	"balance", "loan_balance", "name", "users_count"}

var Statuses = []string{"active", "expired", "disabled", "online"}

func nowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func parseDT(v any) (time.Time, bool) {
	text := val.Str(v)
	if text == "" {
		return time.Time{}, false
	}
	text = val.Cut(text, 19)
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, text, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// flattenUser keeps every plain field SAS4 returned, so the table can offer
// any of them as a column without this code knowing the full list. A nested
// object of plain values is flattened one level ("group": {"name": ..} ->
// "group_name"); lists and deeper nesting are dropped. Empty values are left
// out -- with ~178k users held in memory, that is most of the saving -- and
// read back as blank.
//
// The fields the rest of the app relies on are always set: user_id,
// profile_id, profile_name, online.
func flattenUser(record M) M {
	out := make(M, len(record))
	var nested []string
	for key, value := range record {
		if droppedFields[key] || key == "profile_details" || key == "status" {
			continue
		}
		switch x := value.(type) {
		case M:
			nested = append(nested, key)
		case []any:
		default:
			if !val.Blank(x) {
				out[key] = x
			}
		}
	}
	sort.Strings(nested) // setdefault: plain fields win, then first nested
	for _, key := range nested {
		for sub, inner := range record[key].(M) {
			switch inner.(type) {
			case M, []any:
				continue
			}
			name := key + "_" + sub
			if _, has := out[name]; !has && !val.Blank(inner) {
				out[name] = inner
			}
		}
	}
	details, _ := record["profile_details"].(M)
	out["user_id"] = out["id"]
	delete(out, "id")
	if val.Truthy(record["profile_id"]) {
		out["profile_id"] = record["profile_id"]
	} else {
		out["profile_id"] = details["id"]
	}
	if val.Truthy(details["name"]) {
		out["profile_name"] = details["name"]
	} else {
		out["profile_name"] = record["name"]
	}
	out["online"] = 0
	if val.Str(record["online_status"]) == "1" {
		out["online"] = 1
	}
	delete(out, "online_status")
	return out
}

func flattenManager(record M) M {
	return M{
		"manager_id":       record["id"],
		"manager_username": record["username"],
		"firstname":        record["firstname"],
		"lastname":         record["lastname"],
		"balance":          record["balance"],
		"loan_balance":     record["loan_balance"],
		"users_count":      record["users_count"],
	}
}

type tags map[string]bool

func statusOf(user M, now time.Time) tags {
	t := tags{}
	enabled := val.Str(user["enabled"]) == "1"
	expires, ok := parseDT(user["expiration"])
	if !enabled {
		t["disabled"] = true
	}
	if ok && !expires.After(now) {
		t["expired"] = true
	} else if enabled {
		t["active"] = true
	}
	if val.Truthy(user["online"]) {
		t["online"] = true
	}
	return t
}

func (t tags) sorted() []string {
	out := make([]string, 0, len(t))
	for k := range t {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// fieldRank orders the column picker: the GUI's columns first, then other
// fields alphabetically, then the ones this app adds.
func fieldRank() map[string]int {
	rank := map[string]int{}
	for i, c := range UserColumns {
		rank[c] = i
	}
	for i, c := range []string{"user_id", "profile_id", "profile_name", "online"} {
		rank[c] = 1000 + i
	}
	return rank
}

var userFieldRank = fieldRank()

func orderFields(seen map[string]bool) []string {
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, iok := userFieldRank[out[i]]
		rj, jok := userFieldRank[out[j]]
		if !iok {
			ri = 500
		}
		if !jok {
			rj = 500
		}
		if ri != rj {
			return ri < rj
		}
		return out[i] < out[j]
	})
	return out
}

// Directory holds the users and managers of one SAS4 login, loaded in the
// background.
type Directory struct {
	client   *sas4.Client
	PageSize int

	mu                  sync.RWMutex
	users               []M // the last COMPLETE list
	partial             []M // filling up during a load
	managers            []M
	managerError        any
	fields              []string
	byUsername          map[string]M
	managersByUsername  map[string]M
	state               string // empty | loading | ready | error
	progress            M
	err                 any
	loadedAt            any
	generation          int // bumped on every complete load
	reportCache         []reportUser
	reportCacheGenerate int
}

func NewDirectory(client *sas4.Client, pageSize int) *Directory {
	return &Directory{client: client, PageSize: pageSize, state: "empty",
		progress:           M{"loaded": 0, "total": nil},
		byUsername:         map[string]M{},
		managersByUsername: map[string]M{}, managerError: nil, err: nil,
		reportCacheGenerate: -1}
}

// -- loading ----------------------------------------------------------------

func (d *Directory) State() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.state
}

func (d *Directory) Status() M {
	current := d.CurrentUsers()
	fields := d.Fields()
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := M{"state": d.state, "users": len(d.users),
		"managers": len(d.managers), "error": d.err,
		"manager_error": d.managerError, "browsable": len(current),
		"fields": fields, "loaded_at": d.loadedAt}
	for k, v := range d.progress {
		out[k] = v
	}
	return out
}

// Fields lists every user field present in the data, for the column picker.
// A complete load is scanned once; mid-load, the first rows stand in.
func (d *Directory) Fields() []string {
	d.mu.RLock()
	if len(d.fields) > 0 && len(d.users) > 0 {
		defer d.mu.RUnlock()
		return d.fields
	}
	source := d.users
	if len(source) == 0 {
		source = d.partial
	}
	d.mu.RUnlock()
	seen := map[string]bool{}
	for i, user := range source {
		if i >= 2000 {
			break
		}
		for k := range user {
			seen[k] = true
		}
	}
	return orderFields(seen)
}

// CurrentUsers is what filters run over: the last complete list, or -- on
// the very first load -- the users fetched so far, so the table fills while
// it loads. Jobs never run on a partial list (the server checks ready).
func (d *Directory) CurrentUsers() []M {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if len(d.users) > 0 {
		return d.users
	}
	return d.partial[:len(d.partial):len(d.partial)]
}

func (d *Directory) Users() []M {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.users
}

func (d *Directory) Managers() []M {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.managers
}

func (d *Directory) ByUsername() map[string]M {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.byUsername
}

func (d *Directory) Refresh() {
	d.mu.Lock()
	if d.state == "loading" {
		d.mu.Unlock()
		return
	}
	d.state, d.err = "loading", nil
	d.progress = M{"loaded": 0, "total": nil, "started": nowSeconds()}
	d.partial = nil
	d.mu.Unlock()
	go d.load()
}

func (d *Directory) load() {
	// Users first: they are what every screen needs. Pages are kept as they
	// arrive so the table can show them before the load finishes.
	d.mu.RLock()
	started := d.progress["started"]
	d.mu.RUnlock()
	opt := sas4.DefaultPageOpts()
	opt.PageSize = d.PageSize
	opt.OnPage = func(page int, last any, seen int, total any) {
		var t any
		if n, ok := val.Int(total); ok && n != 0 {
			t = n
		}
		d.mu.Lock()
		d.progress = M{"loaded": seen, "started": started, "total": t}
		d.mu.Unlock()
	}
	err := d.client.Paged("index/user", UserColumns, opt, func(rec M) bool {
		user := flattenUser(rec)
		d.mu.Lock()
		d.partial = append(d.partial, user)
		d.mu.Unlock()
		return true
	})
	if err != nil { // shown in the UI, never fatal
		d.mu.Lock()
		d.state, d.err = "error", err.Error()
		d.mu.Unlock()
		return
	}
	d.mu.RLock()
	partial := d.partial
	d.mu.RUnlock()
	seen := map[string]bool{}
	byUsername := make(map[string]M, len(partial))
	for _, user := range partial {
		for k := range user {
			seen[k] = true
		}
		if val.Truthy(user["username"]) {
			byUsername[val.Fold(strings.TrimSpace(val.Str(user["username"])))] = user
		}
	}
	d.mu.Lock()
	d.fields = orderFields(seen)
	d.users = partial
	d.partial = nil
	d.byUsername = byUsername
	d.generation++
	d.mu.Unlock()
	d.loadManagers()
	d.mu.Lock()
	d.loadedAt = time.Now().Format("2006-01-02 15:04:05")
	d.state = "ready"
	d.mu.Unlock()
}

// loadManagers: managers are optional -- only the Managers operations and the
// manager pickers need them, so a failure here is reported, not fatal. The
// paged POST listing is tried first, then the simple GET list.
func (d *Directory) loadManagers() {
	var managerError any
	var records []M
	got := false
	// One try each, no back-off: users are already usable, and the Run
	// button waits on this.
	opt := sas4.DefaultPageOpts()
	opt.PageSize = d.PageSize
	opt.Retries = 0
	err := d.client.Paged("index/manager", ManagerColumns, opt, func(rec M) bool {
		records = append(records, rec)
		return true
	})
	if err == nil {
		got = true
	} else {
		records = nil
		_, data := d.client.Call("GET", "index/manager", nil, 0)
		var rows []any
		var isList bool
		if obj, ok := data.(M); ok {
			rows, isList = obj["data"].([]any)
		} else {
			rows, isList = data.([]any)
		}
		if isList {
			got = true
			for _, r := range rows {
				if m, ok := r.(M); ok {
					records = append(records, m)
				}
			}
		} else {
			managerError = "manager list unavailable: " + err.Error()
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.managerError = managerError
	if got {
		managers := make([]M, 0, len(records))
		byName := map[string]M{}
		for _, r := range records {
			m := flattenManager(r)
			managers = append(managers, m)
			if val.Truthy(m["manager_username"]) {
				byName[val.Fold(strings.TrimSpace(val.Str(m["manager_username"])))] = m
			}
		}
		d.managers = managers
		d.managersByUsername = byName
	}
}

// -- filtering --------------------------------------------------------------

type UserFilters struct {
	Profiles    IntList  `json:"profiles"`
	Managers    []string `json:"managers"`
	Statuses    []string `json:"statuses"`
	Search      string   `json:"search"`
	ExpiresFrom *string  `json:"expires_from"`
	ExpiresTo   *string  `json:"expires_to"`
	// The rest is applied by App.filterUsers, not FilterUsers: most of it
	// reads each user's last session. See duplicates.go and
	// sessionfilter.go.
	Duplicates    []string `json:"duplicates"`
	MAC           string   `json:"mac"`            // contains; separators ignored
	NasIP         string   `json:"nas_ip"`         // contains
	StopReasons   []string `json:"stop_reasons"`   // any of, exact
	SessionStates []string `json:"session_states"` // online | offline | none
	// Traffic of the last session, in bytes.
	UploadMin   *float64 `json:"upload_min"`
	UploadMax   *float64 `json:"upload_max"`
	DownloadMin *float64 `json:"download_min"`
	DownloadMax *float64 `json:"download_max"`
	TotalMin    *float64 `json:"total_min"`
	TotalMax    *float64 `json:"total_max"`
}

// Active: whether any field narrows the list at all.
func (f UserFilters) Active() bool {
	return len(f.Profiles) > 0 || len(f.Managers) > 0 || len(f.Statuses) > 0 ||
		strings.TrimSpace(f.Search) != "" ||
		(f.ExpiresFrom != nil && *f.ExpiresFrom != "") ||
		(f.ExpiresTo != nil && *f.ExpiresTo != "") ||
		len(cleanKinds(f.Duplicates)) > 0 || sessionFiltered(f)
}

func (d *Directory) FilterUsers(f UserFilters) []M {
	profiles := map[int]bool{}
	for _, p := range f.Profiles {
		profiles[p] = true
	}
	managers := map[string]bool{}
	for _, m := range f.Managers {
		managers[val.Fold(m)] = true
	}
	statuses := map[string]bool{}
	for _, s := range f.Statuses {
		for _, known := range Statuses {
			if s == known {
				statuses[s] = true
			}
		}
	}
	needle := val.Fold(strings.TrimSpace(f.Search))
	var start, end time.Time
	var hasStart, hasEnd bool
	if f.ExpiresFrom != nil {
		start, hasStart = parseDT(*f.ExpiresFrom)
	}
	if f.ExpiresTo != nil {
		end, hasEnd = parseDT(*f.ExpiresTo)
		if hasEnd && len([]rune(*f.ExpiresTo)) <= 10 {
			end = end.Add(23*time.Hour + 59*time.Minute + 59*time.Second)
		}
	}
	now := time.Now()
	out := []M{}
	for _, user := range d.CurrentUsers() {
		if len(profiles) > 0 {
			pid, ok := val.Int(user["profile_id"])
			if !ok || !profiles[pid] {
				continue
			}
		}
		if len(managers) > 0 && !managers[val.Fold(val.Str(user["parent_username"]))] {
			continue
		}
		if len(statuses) > 0 {
			hit := false
			for s := range statusOf(user, now) {
				if statuses[s] {
					hit = true
				}
			}
			if !hit {
				continue
			}
		}
		if needle != "" {
			hit := false
			for _, k := range []string{"username", "firstname", "lastname", "phone"} {
				if strings.Contains(val.Fold(val.Str(user[k])), needle) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		if hasStart || hasEnd {
			expires, ok := parseDT(user["expiration"])
			if !ok || (hasStart && expires.Before(start)) ||
				(hasEnd && expires.After(end)) {
				continue
			}
		}
		out = append(out, user)
	}
	return out
}

func (d *Directory) FilterManagers(search string) []M {
	needle := val.Fold(strings.TrimSpace(search))
	out := []M{}
	for _, m := range d.Managers() {
		if needle == "" {
			out = append(out, m)
			continue
		}
		for _, k := range []string{"manager_username", "firstname", "lastname"} {
			if strings.Contains(val.Fold(val.Str(m[k])), needle) {
				out = append(out, m)
				break
			}
		}
	}
	return out
}

// -- CSV import -------------------------------------------------------------

// MatchImport splits CSV rows into rows to run and rows that could not be
// matched.
//
//	users:     a username column is matched to the directory; the row is the
//	           SAS4 record, with the CSV's columns under "_csv".
//	managers:  manager_username (or username) matched likewise.
//	new_users: kept as they are; a username that already exists is reported
//	           instead, so Create never collides.
func (d *Directory) MatchImport(rows []map[string]string, target string) (matched, unmatched []M) {
	d.mu.RLock()
	byUser, byManager := d.byUsername, d.managersByUsername
	d.mu.RUnlock()
	matched, unmatched = []M{}, []M{}
	for i, row := range rows {
		number := i + 2 // line 1 is the header
		var name string
		var known M
		if target == "managers" {
			name = row["manager_username"]
			if name == "" {
				name = row["username"]
			}
			known = byManager[val.Fold(strings.TrimSpace(name))]
		} else {
			name = row["username"]
			known = byUser[val.Fold(strings.TrimSpace(name))]
		}
		miss := func(reason string) {
			m := M{}
			for k, v := range row {
				m[k] = v
			}
			m["line"], m["reason"] = number, reason
			unmatched = append(unmatched, m)
		}
		switch {
		case name == "":
			miss("no username")
		case target == "new_users":
			if known != nil {
				miss("already exists")
			} else {
				matched = append(matched, M{"username": name, "line": number,
					"_csv": row})
			}
		case known == nil:
			miss("not found in SAS4")
		default:
			// The CSV's values ride separately from the SAS4 record, so a
			// `profile_id` column means "the new profile", never the user's
			// current one.
			m := val.Copy(known)
			m["line"], m["_csv"] = number, row
			matched = append(matched, m)
		}
	}
	return matched, unmatched
}

func csvOf(row M) map[string]string {
	c, _ := row["_csv"].(map[string]string)
	return c
}

// ReadCSVBytes turns an uploaded CSV into rows with lower-cased, trimmed
// headers. Excel on Arabic Windows saves cp1256, so that is tried after
// UTF-8.
func ReadCSVBytes(blob []byte) []map[string]string {
	var text string
	if utf8.Valid(blob) {
		text = strings.TrimPrefix(string(blob), "\ufeff")
	} else if out, err := charmap.Windows1256.NewDecoder().Bytes(blob); err == nil {
		text = string(out)
	} else {
		out, _ := charmap.ISO8859_1.NewDecoder().Bytes(blob)
		text = string(out)
	}
	reader := csv.NewReader(strings.NewReader(text))
	reader.Comma = sniffDelimiter(val.Cut(text, 4096))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	header, err := reader.Read()
	if err != nil {
		return nil
	}
	keys := make([]string, len(header))
	for i, h := range header {
		keys[i] = strings.ToLower(strings.TrimSpace(h))
	}
	rows := []map[string]string{}
	var firsts []string // first value of each kept row, for one-column files
	for {
		rec, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		row := map[string]string{}
		blank := true
		for i, k := range keys {
			v := ""
			if i < len(rec) {
				v = strings.TrimSpace(rec[i])
			}
			row[k] = v
			if v != "" {
				blank = false
			}
		}
		if !blank {
			rows = append(rows, row)
			first := ""
			if len(rec) > 0 {
				first = strings.TrimSpace(rec[0])
			}
			firsts = append(firsts, first)
		}
	}
	// A headerless single column of usernames is the commonest hand-made file.
	if len(keys) == 1 && keys[0] != "username" && keys[0] != "manager_username" {
		out := []map[string]string{{"username": strings.TrimSpace(header[0])}}
		for _, f := range firsts {
			out = append(out, map[string]string{"username": f})
		}
		return out
	}
	return rows
}

// sniffDelimiter picks the one of , ; tab that splits the sample's lines
// into the same number of fields most consistently; ',' when none does.
func sniffDelimiter(sample string) rune {
	lines := []string{}
	for _, l := range strings.Split(sample, "\n") {
		if l = strings.TrimRight(l, "\r"); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > 1 && !strings.HasSuffix(sample, "\n") && len(sample) >= 4096 {
		lines = lines[:len(lines)-1] // probably cut mid-line
	}
	best, bestScore := ',', 0
	for _, d := range []rune{',', ';', '\t'} {
		counts := map[int]int{}
		for _, l := range lines {
			r := csv.NewReader(strings.NewReader(l))
			r.Comma, r.LazyQuotes, r.FieldsPerRecord = d, true, -1
			rec, err := r.Read()
			if err != nil {
				continue
			}
			counts[len(rec)]++
		}
		for fields, n := range counts {
			if fields > 1 && n > bestScore {
				best, bestScore = d, n
			}
		}
	}
	return best
}

// IntList accepts [1, "2", 3.0] like pydantic's List[int].
type IntList []int

func (l *IntList) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		*l = nil
		return nil
	}
	var raw []any
	if err := jsonUnmarshal(b, &raw); err != nil {
		return err
	}
	out := make(IntList, 0, len(raw))
	for _, v := range raw {
		n, ok := val.Int(v)
		if !ok {
			return &badValue{v}
		}
		out = append(out, n)
	}
	*l = out
	return nil
}

type badValue struct{ v any }

func (e *badValue) Error() string { return "not an integer: " + val.Str(e.v) }
