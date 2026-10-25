package app

// An in-memory SAS4 admin API speaking the real wire protocol (encrypted
// {"payload": ...} bodies, Bearer tokens, paged index/* listings), served on
// a local port so the app's real HTTP client talks to it. Every decrypted
// request is recorded in calls.
//
// Users whose username contains "fail" get an error back from any write.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"sastoolkit/internal/sas4"
)

const fakePrefix = "/admin/api/index.php/api/"

type fakeCall struct {
	Method, Path string
	Body         map[string]any
}

type bulkApplied struct {
	body    map[string]any
	matched int
}

type FakeSAS4 struct {
	mu               sync.Mutex
	profiles         []M
	managers         []M
	users            map[int]M
	sessions         []M
	calls            []fakeCall
	tokens           map[string]bool
	logins           int
	rejected         int
	delay            time.Duration
	managersBroken   bool
	ignoreBulkFilter bool
	bulkApplied      []bulkApplied
	server           *httptest.Server
}

func newFake(t *testing.T) *FakeSAS4 {
	f := &FakeSAS4{tokens: map[string]bool{}, users: map[int]M{}}
	f.profiles = []M{{"id": 1, "name": "Flat"}, {"id": 2, "name": "Flat Plus"},
		{"id": 3, "name": "GW:Magnum"}}
	nManagers := 3
	for i := 1; i <= nManagers; i++ {
		f.managers = append(f.managers, M{"id": i, "username": fmt.Sprintf("manager%d", i),
			"firstname": "M", "lastname": strconv.Itoa(i), "balance": "100.00",
			"loan_balance": "0.00", "users_count": 0})
	}
	for i := 1; i <= 150; i++ {
		pid := 1 + i%3
		username := fmt.Sprintf("user%d", i)
		if i == 7 {
			username = "failuser"
		}
		enabled, expiration, online := 1, "2099-01-01 00:00:00", 0
		if i%10 == 0 {
			enabled = 0
		}
		if i%5 == 0 {
			expiration = "2020-01-01 00:00:00"
		}
		if i%4 == 0 {
			online = 1
		}
		f.users[i] = M{
			"id": i, "username": username, "firstname": "U",
			"lastname": strconv.Itoa(i), "phone": fmt.Sprintf("09%08d", i),
			"profile_id": pid, "enabled": enabled, "expiration": expiration,
			"balance": "0.00", "parent_id": 1 + i%nManagers,
			"parent_username": fmt.Sprintf("manager%d", 1+i%nManagers),
			"online_status":   online, "used_traffic": 0, "remaining_days": i,
			"group_name": "G1", "notes": nil,
			"created_at": fmt.Sprintf("2026-09-%02d 10:00:00", 1+i%28),
			"n_row":      i, "status": M{"status": true, "traffic": true},
			"profile_details": M{"id": pid, "name": f.profiles[pid-1]["name"]},
		}
	}
	f.sessions = f.makeSessions()
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *FakeSAS4) URL() string { return f.server.URL }

func pyTime(t time.Time) string { return t.Format("2006-01-02 15:04:05") }

// makeSessions: three sessions per user, a day apart. Every 4th user's newest
// one is still open (online), every 6th user has none, and every 9th only
// has sessions from ~200 days ago.
func (f *FakeSAS4) makeSessions() []M {
	now := time.Now().Truncate(time.Second)
	rows := []M{}
	for i := 1; i <= 150; i++ {
		user := f.users[i]
		if user == nil || i%6 == 0 {
			continue
		}
		back := 0
		if i%9 == 0 {
			back = 200
		}
		base := now.AddDate(0, 0, -back).Add(-time.Duration(10*i) * time.Minute)
		for k := 0; k < 3; k++ {
			start := base.AddDate(0, 0, -k)
			online := k == 0 && i%4 == 0
			var stop any
			cause := ""
			if !online {
				stop = pyTime(start.Add(time.Hour))
				cause = "User-Request"
			}
			rows = append(rows, M{
				"username": user["username"], "acctstarttime": pyTime(start),
				"acctstoptime": stop, "framedipaddress": fmt.Sprintf("10.0.%d.%d", i, k+1),
				"nasipaddress":     "10.255.0.1",
				"callingstationid": fmt.Sprintf("AA:BB:CC:00:%02X:%02X", i%256, k),
				"acctinputoctets":  strconv.Itoa(1000*i + k),
				"acctoutputoctets": strconv.Itoa(5000*i + k),
				"calledstationid":  "pppoe-1", "nasportid": fmt.Sprintf("ether%d", k),
				"acctterminatecause": cause})
		}
	}
	return rows
}

func (f *FakeSAS4) writes(route string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []fakeCall{}
	for _, c := range f.calls {
		if c.Method != "GET" && !strings.HasPrefix(c.Path, "index/") &&
			c.Path != "login" && (route == "" || c.Path == route) {
			out = append(out, c)
		}
	}
	return out
}

func (f *FakeSAS4) callsTo(path string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []fakeCall{}
	for _, c := range f.calls {
		if c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

func (f *FakeSAS4) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (f *FakeSAS4) handle(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, fakePrefix)
	raw, _ := io.ReadAll(r.Body)
	body := map[string]any{}
	if len(raw) > 0 {
		var wrapped map[string]string
		json.Unmarshal(raw, &wrapped)
		plain, err := sas4.Decrypt(wrapped["payload"], sas4.APISecret)
		if err != nil {
			reply(w, 400, M{"message": "bad payload"})
			return
		}
		json.Unmarshal([]byte(plain), &body)
	}
	record := fakeCall{r.Method, path, body}
	if path == "login" {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, record)
		if body["password"] != "secret" {
			reply(w, 401, M{"status": 401, "message": "bad"})
			return
		}
		token := fmt.Sprintf("tok%d-%d", len(f.tokens), time.Now().UnixNano())
		f.tokens[token] = true
		f.logins++
		reply(w, 200, M{"status": 200, "token": token})
		return
	}
	f.mu.Lock()
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !f.tokens[auth] {
		f.rejected++ // never reached the handler
		f.mu.Unlock()
		reply(w, 401, M{"message": "Unauthenticated."})
		return
	}
	f.calls = append(f.calls, record)
	delay := f.delay
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	f.mu.Lock()
	code, out := f.route(r.Method, path, body)
	f.mu.Unlock()
	reply(w, code, out)
}

func (f *FakeSAS4) page(records []M, body map[string]any) M {
	page, count := 1, 10
	if p, ok := body["page"].(float64); ok {
		page = int(p)
	}
	if c, ok := body["count"].(float64); ok {
		count = int(c)
	}
	from, to := min(len(records), (page-1)*count), min(len(records), page*count)
	last := max(1, (len(records)+count-1)/count)
	return M{"current_page": page, "data": records[from:to], "last_page": last,
		"total": len(records)}
}

func (f *FakeSAS4) sortedUsers() []M {
	ids := []int{}
	for id := range f.users {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	out := []M{}
	for _, id := range ids {
		out = append(out, f.users[id])
	}
	return out
}

func asFilters(v any) []map[string]any {
	out := []map[string]any{}
	list, _ := v.([]any)
	for _, x := range list {
		m, _ := x.(map[string]any)
		out = append(out, m)
	}
	return out
}

func (f *FakeSAS4) route(method, path string, body map[string]any) (int, any) {
	okBody := M{"status": 200, "message": "done"}
	parts := strings.Split(path, "/")
	switch {
	case path == "index/user":
		users := f.sortedUsers()
		if filters := asFilters(body["bulk_filter"]); len(filters) > 0 && !f.ignoreBulkFilter {
			kept := []M{}
			for _, u := range users {
				if bulkMatch(u, filters) {
					kept = append(kept, u)
				}
			}
			users = kept
		}
		return 200, f.page(users, body)
	case path == "index/UserSessions":
		rows := []M{}
		search, _ := body["search"].(string)
		for _, s := range f.sessions {
			if search == "" || strings.Contains(s["username"].(string), search) {
				rows = append(rows, s)
			}
		}
		key, _ := body["sortBy"].(string)
		if key == "" {
			key = "acctstarttime"
		}
		desc := body["direction"] == "desc"
		sort.SliceStable(rows, func(i, j int) bool {
			a, b := fmt.Sprint(rows[i][key]), fmt.Sprint(rows[j][key])
			if desc {
				return a > b
			}
			return a < b
		})
		return 200, f.page(rows, body)
	case path == "bulkChanges/users/apply":
		filters := asFilters(body["filter"])
		action, _ := body["action"].(map[string]any)
		matched := 0
		for _, u := range f.users {
			if !bulkMatch(u, filters) {
				continue
			}
			matched++
			switch action["name"] {
			case "set_traffic_balance":
				u["traffic_balance"] = action["option"]
			case "disable":
				u["enabled"] = 0
			}
		}
		f.bulkApplied = append(f.bulkApplied, bulkApplied{body, matched})
		return 200, M{"status": 200, "message": fmt.Sprintf("%d users updated", matched)}
	case path == "index/manager":
		if f.managersBroken {
			return 500, M{"message": "boom"}
		}
		if method == "GET" {
			return 200, M{"status": 200, "data": f.managers}
		}
		return 200, f.page(f.managers, body)
	case path == "list/profile/0":
		return 200, M{"status": 200, "data": f.profiles}
	case path == "site":
		return 200, M{"status": 200, "data": []M{{"id": 1, "name": "Main"}}}
	case parts[0] == "allowedExtensions":
		return 200, M{"status": 200, "data": []M{{"id": 9, "name": "Extra week"}}}
	}

	var user M
	if v, ok := body["user_id"]; ok && v != nil {
		id, _ := strconv.Atoi(fmt.Sprint(v))
		user = f.users[id]
	} else if len(parts) >= 2 && parts[0] == "user" {
		if id, err := strconv.Atoi(parts[len(parts)-1]); err == nil {
			user = f.users[id]
		}
	}
	if user != nil && strings.Contains(user["username"].(string), "fail") && method != "GET" {
		return 200, M{"status": -1, "message": "refused by fake"}
	}

	switch path {
	case "user/addTraffic", "user/deposit", "user/withdraw", "user/activate",
		"user/extend", "user/changeProfile":
		if user == nil {
			return 404, M{"status": 404, "message": "no user"}
		}
		if path == "user/changeProfile" {
			pid, _ := strconv.Atoi(fmt.Sprint(body["profile_id"]))
			user["profile_id"] = pid
		}
		return 200, okBody
	}
	if path == "user" && method == "POST" {
		newID := 0
		for id := range f.users {
			newID = max(newID, id)
		}
		newID++
		u := M{"id": newID, "profile_details": M{}}
		for k, v := range body {
			u[k] = v
		}
		f.users[newID] = u
		return 200, okBody
	}
	if parts[0] == "user" && len(parts) > 1 && parts[1] == "rename" {
		id, _ := strconv.Atoi(parts[2])
		f.users[id]["username"] = body["new_username"]
		return 200, okBody
	}
	if parts[0] == "user" && len(parts) == 2 {
		if id, err := strconv.Atoi(parts[1]); err == nil {
			switch method {
			case "DELETE":
				delete(f.users, id)
				return 200, okBody
			case "GET":
				rec := M{}
				for k, v := range f.users[id] {
					if k != "profile_details" {
						rec[k] = v
					}
				}
				return 200, M{"status": 200, "data": rec}
			}
			f.users[id]["enabled"] = body["enabled"]
			return 200, okBody
		}
	}
	if parts[0] == "user" && len(parts) > 1 {
		switch parts[1] {
		case "refund", "refundData", "activationData", "extensionData":
			return 200, M{"status": 200, "data": M{"amount": 5}}
		}
	}
	if parts[0] == "manager" {
		return 200, okBody
	}
	return 404, M{"message": "no route " + path}
}

func bulkMatch(user M, filters []map[string]any) bool {
	for _, f := range filters {
		have, want := fmt.Sprint(user[f["key"].(string)]), fmt.Sprint(f["value"])
		switch f["op"] {
		case "=":
			if have != want {
				return false
			}
		case "!=":
			if have == want {
				return false
			}
		case "like":
			if !strings.Contains(have, strings.Trim(want, "%")) {
				return false
			}
		}
	}
	return true
}
