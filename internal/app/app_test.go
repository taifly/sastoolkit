package app

// End-to-end tests: the real app, over real HTTP, against fakesas4_test.go.

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"sastoolkit/internal/sas4"
)

// Captured from the SAS4 admin GUI's "Add Traffic".
const captured = "U2FsdGVkX1/aa4G5MsJpXT4ETVGOSVjLV3Gc3LSd/SR4kAUmS055IkFRwMMjHiNemhzy" +
	"YKPnySW3L5RDWREEFRJ9rEtUydI3Vhr+Yfpsf1ZWSUpBiyNwv+dern080WokZlTRuUX2" +
	"FZhCaQbBDqBBx/aqHegg1IF3qllWK3ygoKjdkSXO9jdZimZKZnG9gwR3DSWN+XAA6/Z/" +
	"4dIg+fMoRA=="

// Captured from the SAS4 admin GUI's Bulk Changes screen.
const capturedBulk = "U2FsdGVkX1+taZG4JNzGUA9fxAKj6nWqJ8UQEa/x0am2z7Vt9T0Zw/3t9lcz3jykWSCqV+kI" +
	"gVmk3JxfK1FxxCCamqiUPgaIHjhvYVxHRC+YWbGWC4qWhIrRBDbzJ61/reBqJo0nh8dDNnlD" +
	"coSFH88AClji5N1+cvFw3gkhlHFBT50jfyR4lG/FXc/J3NvQfsJJ17tKBsoO71boodX0+deu" +
	"Fb7oYaOx8f7ayBORPZNfYfdHgHTU8JlNgXB3eIjo7eWGDp2WV8nJfPlpo9OkgA=="

const capturedIndexUser = "U2FsdGVkX1/pZpXSFSookLFpziZLXsIeAwXCvj8ehlElfhiWjKNPE26weMHpCTaFDlW0ewzo" +
	"5BpcfQIbAtBUmBkxtJXXAP2l4pj9Ay5JAM8RfdY3oMioG21MtFbMEREhsd3kufrSPXpjSgMT" +
	"vs8YITZTsPWzAn6j/2x1kYpYi33x3VDc6YJLNAAkxVUh6OCRK6fOy9NTXzjs0dSOm1lxrBur" +
	"yBMnK3d/Vy5prCxNe1Fnpft7GK2TINLFjHqDHb79Hz+Ak+xfTbZFzk+0t4CEOAPdqrbzejhp" +
	"FTEgE1CKJG0bidQYJjpWla68P01Ki0/oLfLFbRUUs527I1JIG8pF2x6GfK2+GzLfteQdCGVI" +
	"X8XjwBaX57bKuLeHsx7IweWbzwgzRtoZ3jXxgqPPZjERTi7F741hzhaiPEkXFVMtG95lx6Pc" +
	"B8D+bn30WjVu/rG7WDVinJU3MiETnRPGx3MmxJPFcqvI+n4qDVK4AE+LvZq6l0G0+3cguJg8" +
	"fkJRGiQi6tJRw0ZXlZIUST6wV8iR+vdjvhOWD4t14uxwlCQmF3CezZjyQZb9g4aTm6x47F8e" +
	"E9g7vfjx1LeCgqDqHQ81kXXnABZyrnIwbe9lJu7vwl/056IW9hv6ckIBMng2CZQpvoslEDhn" +
	"G9GnaXwEwa2MDw=="

// --------------------------------------------------------------------------
// HARNESS
// --------------------------------------------------------------------------

type harness struct {
	t      *testing.T
	fake   *FakeSAS4
	client *http.Client
	base   string
	dir    string
}

type resp struct {
	code   int
	header http.Header
	raw    []byte
	json   M
}

func (r resp) str(key string) string { return fmt.Sprint(r.json[key]) }

func newHarness(t *testing.T, fake *FakeSAS4, cfg Config) *harness {
	t.Helper()
	dir := t.TempDir()
	ReportsDir = dir
	if cfg.PageSize == 0 {
		cfg.PageSize = 40
	}
	srv := httptest.NewServer(New(cfg))
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &harness{t: t, fake: fake, client: &http.Client{Jar: jar},
		base: srv.URL, dir: dir}
}

func (h *harness) do(req *http.Request) resp {
	h.t.Helper()
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{code: res.StatusCode, header: res.Header, raw: raw}
	json.Unmarshal(raw, &out.json)
	return out
}

func (h *harness) get(path string) resp {
	req, _ := http.NewRequest("GET", h.base+path, nil)
	return h.do(req)
}

func (h *harness) post(path string, body any) resp {
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest("POST", h.base+path, reader)
	req.Header.Set("Content-Type", "application/json")
	return h.do(req)
}

func (h *harness) upload(op, name, data string) resp {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("op", op)
	part, _ := w.CreateFormFile("file", name)
	part.Write([]byte(data))
	w.Close()
	req, _ := http.NewRequest("POST", h.base+"/api/import", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return h.do(req)
}

func (h *harness) login() {
	h.t.Helper()
	r := h.post("/api/login", M{"username": "admin", "password": "secret"})
	if r.code != 200 {
		h.t.Fatalf("login: %d %s", r.code, r.raw)
	}
}

func (h *harness) directory() M { return h.get("/api/directory").json }

func wait(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()
	end := time.Now().Add(timeout)
	for time.Now().Before(end) {
		if predicate() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func (h *harness) waitReady() {
	wait(h.t, 40*time.Second, func() bool { return h.directory()["state"] == "ready" })
}

// app logs in to a fresh fake and waits for the user list.
func app(t *testing.T) (*harness, *FakeSAS4) {
	fake := newFake(t)
	h := newHarness(t, fake, Config{BaseURL: fake.URL(), AllowURLChange: true})
	h.login()
	h.waitReady()
	return h, fake
}

func (h *harness) waitJob(id any, states ...string) M {
	h.t.Helper()
	path := fmt.Sprintf("/api/jobs/%v", id)
	wait(h.t, 15*time.Second, func() bool {
		state := h.get(path).json["state"]
		for _, s := range states {
			if state == s {
				return true
			}
		}
		return false
	})
	return h.get(path).json
}

func (h *harness) runJob(body M) M {
	h.t.Helper()
	r := h.post("/api/jobs", body)
	if r.code != 200 {
		h.t.Fatalf("job: %d %s", r.code, r.raw)
	}
	return h.waitJob(jsonID(r.json["id"]), "done", "cancelled")
}

func jsonID(v any) int { return int(v.(float64)) }

func (h *harness) reportRows(summary M) []map[string]string {
	h.t.Helper()
	path := filepath.Join(h.dir, filepath.Base(summary["report"].(string)))
	raw, err := os.ReadFile(path)
	if err != nil {
		h.t.Fatal(err)
	}
	return csvDicts(h.t, strings.TrimPrefix(string(raw), "\ufeff"))
}

func csvDicts(t *testing.T, text string) []map[string]string {
	rows := csvRows(t, text)
	out := []map[string]string{}
	for _, r := range rows[1:] {
		m := map[string]string{}
		for i, k := range rows[0] {
			m[k] = r[i]
		}
		out = append(out, m)
	}
	return out
}

func csvRows(t *testing.T, text string) [][]string {
	t.Helper()
	rows, err := csv.NewReader(strings.NewReader(text)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func byProfile(fake *FakeSAS4, pid int) []M {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	out := []M{}
	for _, u := range fake.sortedUsers() {
		if fmt.Sprint(u["profile_id"]) == fmt.Sprint(pid) {
			out = append(out, u)
		}
	}
	return out
}

func counts(job M) map[string]int {
	out := map[string]int{}
	for k, v := range job["counts"].(map[string]any) {
		out[k] = int(v.(float64))
	}
	return out
}

func eq[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

func eqCounts(t *testing.T, job M, want map[string]int) {
	t.Helper()
	got := counts(job)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("counts: got %v, want %v", got, want)
	}
}

func decrypted(t *testing.T, blob string) map[string]any {
	plain, err := sas4.Decrypt(blob, sas4.APISecret)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.Unmarshal([]byte(plain), &out)
	return out
}

// roundTrip passes a built payload through JSON, as the fake receives it.
func roundTrip(v any) map[string]any {
	raw, _ := json.Marshal(v)
	var out map[string]any
	json.Unmarshal(raw, &out)
	return out
}

// --------------------------------------------------------------------------

func TestCryptoRoundTrip(t *testing.T) {
	enc, err := sas4.Encrypt(`{"a":"ليبيا"}`, sas4.APISecret)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := sas4.Decrypt(enc, sas4.APISecret)
	eq(t, err, nil, "decrypt")
	eq(t, plain, `{"a":"ليبيا"}`, "round trip")
}

func TestAddTrafficPayloadMatchesTheCapturedGUIRequest(t *testing.T) {
	want := decrypted(t, captured)
	built, err := buildAddTraffic(M{"user_id": 2, "username": "test"},
		M{"amount": "2", "unit": "GB", "comment": "Demo", "target": "rxtx_mbytes"}, Ctx{})
	if err != nil {
		t.Fatal(err)
	}
	got := roundTrip(built)
	if len(got) != len(want) {
		t.Fatalf("keys: got %v, want %v", got, want)
	}
	for _, k := range []string{"user_id", "username", "amount", "comment", "target"} {
		eq(t, fmt.Sprint(got[k]), fmt.Sprint(want[k]), k)
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Fatalf("missing key %s", k)
		}
	}
}

func TestBadPasswordIsRefused(t *testing.T) {
	fake := newFake(t)
	h := newHarness(t, fake, Config{BaseURL: fake.URL(), AllowURLChange: true})
	eq(t, h.post("/api/login", M{"username": "admin", "password": "x"}).code, 401, "login")
	eq(t, h.get("/api/meta").code, 401, "meta")
}

func TestDirectoryPagesThroughEveryoneAndFilters(t *testing.T) {
	h, fake := app(t)
	status := h.directory()
	eq(t, int(status["users"].(float64)), 150, "users")
	eq(t, int(status["managers"].(float64)), 3, "managers")
	r := h.post("/api/users/query", M{"filters": M{"profiles": []int{1}}, "page_size": 500}).json
	eq(t, int(r["total"].(float64)), len(byProfile(fake, 1)), "profile 1")
	r = h.post("/api/users/query", M{"filters": M{"statuses": []string{"disabled"}},
		"page_size": 500}).json
	eq(t, int(r["total"].(float64)), 15, "disabled")
	r = h.post("/api/users/query", M{"filters": M{"search": "failuser"}}).json
	rows := r["rows"].([]any)
	eq(t, len(rows), 1, "search")
	eq(t, rows[0].(M)["username"], any("failuser"), "search hit")
	// profile ids sent as strings are accepted too
	r = h.post("/api/users/query", M{"filters": M{"profiles": []string{"1"}}}).json
	eq(t, int(r["total"].(float64)), len(byProfile(fake, 1)), "string profile id")
}

func TestDryRunSendsNothing(t *testing.T) {
	h, fake := app(t)
	job := h.runJob(M{"op": "add_traffic", "dry_run": true,
		"params": M{"amount": 1, "unit": "GB"},
		"source": M{"type": "filter", "filters": M{"profiles": []int{1}}}})
	eqCounts(t, job, map[string]int{"would_run": len(byProfile(fake, 1))})
	eq(t, len(fake.writes("")), 0, "writes")
	for _, r := range h.reportRows(job) {
		eq(t, r["action"], "would_run", "report action")
	}
}

func TestLiveAddTrafficSendsEachUserOnceAndRetryOnlyFailed(t *testing.T) {
	h, fake := app(t)
	targets := byProfile(fake, 2) // includes failuser (id 7)
	job := h.runJob(M{"op": "add_traffic", "dry_run": false, "workers": 8,
		"params": M{"amount": 1.5, "unit": "GB", "target": "rxtx_mbytes", "comment": "t"},
		"source": M{"type": "filter", "filters": M{"profiles": []int{2}}}})
	sent := fake.writes("user/addTraffic")
	eq(t, len(sent), len(targets), "sent")
	gotIDs, wantIDs := []int{}, []int{}
	txs := map[string]bool{}
	for _, c := range sent {
		gotIDs = append(gotIDs, int(c.Body["user_id"].(float64)))
		eq(t, c.Body["amount"], any(1536.0), "amount")
		txs[c.Body["transaction_id"].(string)] = true
	}
	for _, u := range targets {
		wantIDs = append(wantIDs, u["id"].(int))
	}
	sort.Ints(gotIDs)
	eq(t, fmt.Sprint(gotIDs), fmt.Sprint(wantIDs), "user ids")
	eq(t, len(txs), len(sent), "unique transaction ids")
	eqCounts(t, job, map[string]int{"done": len(targets) - 1, "failed": 1})
	rows := h.reportRows(job)
	eq(t, len(rows), len(targets), "report rows")
	failed := []string{}
	for _, r := range rows {
		if r["action"] == "failed" {
			failed = append(failed, r["username"])
		}
	}
	eq(t, fmt.Sprint(failed), "[failuser]", "failed rows")

	r := h.post(fmt.Sprintf("/api/jobs/%d/retry-failed", jsonID(job["id"])), nil)
	eq(t, r.code, 200, string(r.raw))
	h.waitJob(jsonID(r.json["id"]), "done")
	eq(t, len(fake.writes("user/addTraffic")), len(targets)+1, "after retry")
}

func TestChangeProfileUsesTheFormProfileNotTheCurrentOne(t *testing.T) {
	h, fake := app(t)
	h.runJob(M{"op": "change_profile", "dry_run": false,
		"params": M{"profile_id": "3", "method": "credit"},
		"source": M{"type": "filter", "filters": M{"profiles": []int{1}}, "ids": []int{3}}})
	calls := fake.writes("user/changeProfile")
	eq(t, len(calls), 1, "calls")
	eq(t, calls[0].Body["profile_id"], any("3"), "profile_id")
	eq(t, calls[0].Body["user_id"], any("3"), "user_id")
}

func TestTickedIDsLimitTheSelection(t *testing.T) {
	h, fake := app(t)
	h.runJob(M{"op": "delete", "dry_run": false, "params": M{},
		"source": M{"type": "filter", "filters": M{}, "ids": []int{4, 8}}})
	paths := []string{}
	for _, c := range fake.writes("") {
		paths = append(paths, c.Path)
	}
	sort.Strings(paths)
	eq(t, fmt.Sprint(paths), "[user/4 user/8]", "paths")
	fake.set(func() {
		eq(t, fake.users[4] == nil && fake.users[8] == nil, true, "deleted")
	})
}

func TestCSVImportMatchesUsernamesAndUsesPerRowAmounts(t *testing.T) {
	h, fake := app(t)
	r := h.upload("deposit", "d.csv", "Username,Amount\nuser1,100\nUSER2,\nnobody,5\n").json
	eq(t, int(r["matched"].(float64)), 2, "matched")
	eq(t, int(r["unmatched_count"].(float64)), 1, "unmatched")
	eq(t, r["unmatched"].([]any)[0].(M)["reason"], any("not found in SAS4"), "reason")
	h.runJob(M{"op": "deposit", "dry_run": false,
		"params": M{"amount": "7", "comment": "c"},
		"source": M{"type": "import", "import_id": r["import_id"]}})
	amounts := map[string]any{}
	for _, c := range fake.writes("user/deposit") {
		amounts[c.Body["user_username"].(string)] = c.Body["amount"]
	}
	eq(t, fmt.Sprint(amounts), "map[user1:100 user2:7]", "amounts")
}

func TestCreateSkipsExistingUsernamesAndResolvesNames(t *testing.T) {
	h, fake := app(t)
	r := h.upload("create", "c.csv", "username,password,profile_id,firstname\n"+
		"new1,pw1,Flat Plus,Ali\nuser1,pw,Flat,X\nnew2,,,\n").json
	eq(t, int(r["matched"].(float64)), 2, "matched")
	eq(t, r["unmatched"].([]any)[0].(M)["reason"], any("already exists"), "reason")
	job := h.runJob(M{"op": "create", "dry_run": false,
		"params": M{"password": "default", "profile_id": "1", "parent_id": "2", "enabled": true},
		"source": M{"type": "import", "import_id": r["import_id"]}})
	bodies := map[string]map[string]any{}
	for _, c := range fake.writes("user") {
		bodies[c.Body["username"].(string)] = c.Body
	}
	eq(t, bodies["new1"]["profile_id"], any(2.0), "new1 profile")
	eq(t, bodies["new1"]["password"], any("pw1"), "new1 password")
	eq(t, bodies["new1"]["firstname"], any("Ali"), "new1 firstname")
	eq(t, bodies["new1"]["parent_id"], any(2.0), "new1 parent")
	eq(t, bodies["new2"]["profile_id"], any(1.0), "new2 profile")
	eq(t, bodies["new2"]["password"], any("default"), "new2 password")
	eqCounts(t, job, map[string]int{"done": 2})
}

func TestUnverifiedOperationRunsLiveOnOneRowOnly(t *testing.T) {
	h, fake := app(t)
	body := M{"op": "set_enabled", "dry_run": false, "params": M{"enabled": "0"},
		"source": M{"type": "filter", "filters": M{}, "ids": []int{1, 2}}}
	r := h.post("/api/jobs", body)
	eq(t, r.code, 400, "code")
	eq(t, strings.Contains(r.str("detail"), "--allow-unverified"), true, r.str("detail"))
	body["source"].(M)["ids"] = []int{1}
	job := h.runJob(body)
	eqCounts(t, job, map[string]int{"done": 1})
	fake.set(func() { eq(t, fmt.Sprint(fake.users[1]["enabled"]), "0", "enabled") })
}

func TestExpiredTokenLogsInAgainMidRun(t *testing.T) {
	h, fake := app(t)
	var logins int
	fake.set(func() { logins = fake.logins; fake.tokens = map[string]bool{} })
	job := h.runJob(M{"op": "m_deposit", "dry_run": false,
		"params": M{"amount": 10, "is_loan": true}, "source": M{"type": "managers"}})
	eqCounts(t, job, map[string]int{"done": 3})
	fake.set(func() {
		eq(t, fake.logins > logins && fake.rejected >= 1, true, "re-login")
	})
	bodies := fake.writes("manager/deposit")
	eq(t, len(bodies), 3, "deposits")
	for _, b := range bodies {
		eq(t, b.Body["is_loan"], any(true), "is_loan")
	}
}

func TestAWriteThatTimesOutIsNotSentTwice(t *testing.T) {
	fake := newFake(t)
	client := sas4.New(fake.URL(), "admin", "secret",
		sas4.Options{ConnectTimeout: 5 * time.Second, ReadTimeout: 300 * time.Millisecond})
	if err := client.Login(); err != nil {
		t.Fatal(err)
	}
	fake.set(func() { fake.delay = time.Second })
	result := Execute(client, ByKey["add_traffic"], M{"user_id": 1, "username": "user1"},
		M{"amount": 1, "unit": "GB"}, Ctx{}, false)
	fake.set(func() { fake.delay = 0 })
	time.Sleep(1200 * time.Millisecond)
	eq(t, result["action"], any("failed"), "action")
	eq(t, len(fake.writes("user/addTraffic")), 1, "writes")
}

func TestMissingRequiredFieldIsRejected(t *testing.T) {
	h, _ := app(t)
	r := h.post("/api/jobs", M{"op": "add_traffic", "dry_run": true, "params": M{},
		"source": M{"type": "filter", "filters": M{}}})
	eq(t, r.code, 400, "code")
	eq(t, strings.Contains(r.str("detail"), "Amount"), true, r.str("detail"))
}

func TestPageIsServed(t *testing.T) {
	h, _ := app(t)
	eq(t, strings.Contains(string(h.get("/").raw), "SAS toolkit"), true, "index")
	eq(t, h.get("/static/app.js").code, 200, "app.js")
	eq(t, h.get("/nope").code, 404, "unknown path")
}

// --------------------------------------------------------------------------
// server-side bulk change
// --------------------------------------------------------------------------

var flatFilter = []M{{"key": "profile_id", "op": "=", "value": "1"}}

func TestBulkBodyMatchesTheCapturedGUIRequest(t *testing.T) {
	want := decrypted(t, capturedBulk)
	ctx := Ctx{"profiles": Lookup{ByID: map[int]string{14: "flat-Old"}, ByName: map[string]int{}}}
	filters, err := BuildFilter([]BulkFilterRow{{"profile_id", "=", "14"}}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	action, err := BuildAction("set_traffic_balance", "92160")
	if err != nil {
		t.Fatal(err)
	}
	strip := func(rows any) string {
		out := []map[string]any{}
		for _, r := range rows.([]any) {
			m := r.(map[string]any)
			delete(m, "id")
			out = append(out, m)
		}
		return fmt.Sprint(out)
	}
	got := roundTrip(M{"filter": filters, "action": action})
	if _, ok := got["filter"].([]any)[0].(map[string]any)["id"]; !ok {
		t.Fatal("filter rows need an id")
	}
	eq(t, strip(got["filter"]), strip(want["filter"]), "filter")
	eq(t, fmt.Sprint(got["action"]), fmt.Sprint(want["action"]), "action")
	disable, _ := BuildAction("disable", "ignored")
	eq(t, fmt.Sprint(disable), "map[name:disable]", "no-value action")
}

func TestBulkPreviewCountsAndApplySendsOneRequest(t *testing.T) {
	h, fake := app(t)
	flat := byProfile(fake, 1)
	r := h.post("/api/bulk/preview", M{"filters": flatFilter}).json
	eq(t, int(r["total"].(float64)), len(flat), "total")
	eq(t, len(r["problems"].([]any)), 0, "problems")
	eq(t, len(r["sample"].([]any)), min(20, len(flat)), "sample")

	res := h.post("/api/bulk/apply", M{"filters": flatFilter, "action": "set_traffic_balance",
		"option": 92160, "expected_count": len(flat)})
	eq(t, res.code, 200, string(res.raw))
	job := h.waitJob(jsonID(res.json["id"]), "done")
	eqCounts(t, job, map[string]int{"done": len(flat)})
	fake.set(func() {
		eq(t, len(fake.bulkApplied), 1, "applied")
		applied := fake.bulkApplied[0]
		eq(t, applied.matched, len(flat), "matched")
		eq(t, fmt.Sprint(applied.body["action"]), "map[name:set_traffic_balance option:92160]", "action")
		f := applied.body["filter"].([]any)[0].(map[string]any)
		eq(t, f["value"], any(1.0), "value")
		eq(t, f["value_label"], any("Flat"), "label")
		for _, u := range flat {
			eq(t, fmt.Sprint(u["traffic_balance"]), "92160", "traffic balance")
		}
	})
	rows := h.reportRows(job)
	eq(t, rows[0]["action"], "done", "report action")
	eq(t, rows[0]["users"], fmt.Sprint(len(flat)), "report users")
}

func TestBulkApplyRefusesWhenTheCountChanged(t *testing.T) {
	h, fake := app(t)
	r := h.post("/api/bulk/apply", M{"filters": flatFilter, "action": "disable",
		"expected_count": len(byProfile(fake, 1)) + 1})
	job := h.waitJob(jsonID(r.json["id"]), "done")
	eqCounts(t, job, map[string]int{"refused": 1})
	msg := job["recent"].([]any)[0].(M)["message"].(string)
	eq(t, strings.Contains(msg, "preview again"), true, msg)
	fake.set(func() { eq(t, len(fake.bulkApplied), 0, "applied") })
}

func TestBulkApplyRefusesWhenTheServerIgnoresTheFilter(t *testing.T) {
	h, fake := app(t)
	fake.set(func() { fake.ignoreBulkFilter = true })
	r := h.post("/api/bulk/preview", M{"filters": flatFilter}).json
	eq(t, len(r["problems"].([]any)) > 0, true, "problems")
	res := h.post("/api/bulk/apply", M{"filters": flatFilter, "action": "delete",
		"expected_count": r["total"]})
	job := h.waitJob(jsonID(res.json["id"]), "done")
	eqCounts(t, job, map[string]int{"refused": 1})
	fake.set(func() { eq(t, len(fake.bulkApplied), 0, "applied") })
}

func TestBulkNeedsAFilterAndAValue(t *testing.T) {
	h, fake := app(t)
	r := h.post("/api/bulk/preview", M{"filters": []M{}})
	eq(t, r.code, 400, "empty filter")
	eq(t, strings.Contains(r.str("detail"), "every user"), true, r.str("detail"))
	r = h.post("/api/bulk/apply", M{"filters": flatFilter, "action": "add_traffic",
		"option": "", "expected_count": 5})
	eq(t, r.code, 400, "no option")
	eq(t, strings.Contains(r.str("detail"), "needs a value"), true, r.str("detail"))
	r = h.post("/api/bulk/preview", M{"filters": []M{{"key": "profile_id", "op": "=", "value": ""}}})
	eq(t, r.code, 400, "blank value")
	fake.set(func() { eq(t, len(fake.bulkApplied), 0, "applied") })
}

// --------------------------------------------------------------------------
// loading the user list after login
// --------------------------------------------------------------------------

func loggedIn(t *testing.T, fake *FakeSAS4) *harness {
	h := newHarness(t, fake, Config{BaseURL: fake.URL(), AllowURLChange: true})
	h.login()
	return h
}

func TestUsersLoadEvenWhenTheManagerListFails(t *testing.T) {
	fake := newFake(t)
	fake.managersBroken = true
	h := loggedIn(t, fake)
	h.waitReady()
	status := h.directory()
	eq(t, int(status["users"].(float64)), 150, "users")
	eq(t, int(status["managers"].(float64)), 0, "managers")
	eq(t, status["manager_error"] != nil, true, "manager_error")
	r := h.post("/api/users/query", M{"filters": M{}}).json
	eq(t, int(r["total"].(float64)), 150, "total")
	eq(t, r["partial"], any(false), "partial")
}

func TestUsersCanBeBrowsedWhileLoadingButNotRunOn(t *testing.T) {
	fake := newFake(t)
	fake.delay = 150 * time.Millisecond // ~4 slow pages of 40
	h := loggedIn(t, fake)
	wait(t, 15*time.Second, func() bool { return h.directory()["browsable"].(float64) > 0 })
	eq(t, h.directory()["state"], any("loading"), "state")
	r := h.post("/api/users/query", M{"filters": M{}}).json
	total := int(r["total"].(float64))
	eq(t, r["partial"], any(true), "partial")
	eq(t, total > 0 && total < 150, true, "partial total")
	res := h.post("/api/jobs", M{"op": "add_traffic", "dry_run": true,
		"params": M{"amount": 1}, "source": M{"type": "filter", "filters": M{}}})
	eq(t, res.code, 409, "job while loading")
	fake.set(func() { fake.delay = 0 })
	h.waitReady()
	r = h.post("/api/users/query", M{"filters": M{}}).json
	eq(t, r["partial"], any(false), "partial after")
	eq(t, int(r["total"].(float64)), 150, "total after")
}

func TestUserListAsksForTheGUIColumnsAndKeepsExtraFields(t *testing.T) {
	h, fake := app(t)
	want := decrypted(t, capturedIndexUser)
	sent := fake.callsTo("index/user")[0].Body
	eq(t, fmt.Sprint(sent["columns"]), fmt.Sprint(want["columns"]), "columns")

	fields := map[string]bool{}
	for _, f := range h.directory()["fields"].([]any) {
		fields[f.(string)] = true
	}
	for _, k := range []string{"remaining_days", "group_name", "username", "user_id", "profile_name"} {
		eq(t, fields[k], true, "has "+k)
	}
	for _, k := range []string{"n_row", "notes", "status", "profile_details", "id"} {
		eq(t, fields[k], false, "lacks "+k)
	}
	rows := h.post("/api/users/query", M{"filters": M{"search": "user3"}, "page_size": 1}).json["rows"].([]any)
	eq(t, len(rows), 1, "rows")
	row := rows[0].(M)
	eq(t, row["remaining_days"], any(3.0), "remaining_days")
	eq(t, row["group_name"], any("G1"), "group_name")
}

// --------------------------------------------------------------------------
// export and load progress
// --------------------------------------------------------------------------

var exportCols = []M{{"key": "username", "label": "Username"},
	{"key": "fullname", "label": "Name"}, {"key": "status", "label": "Status"},
	{"key": "phone", "label": "Phone"}}

func readExport(t *testing.T, r resp) [][]string {
	t.Helper()
	text := string(r.raw)
	if !strings.HasPrefix(text, "\ufeff") { // Excel needs the BOM
		t.Fatalf("no BOM: %q", val50(text))
	}
	return csvRows(t, strings.TrimPrefix(text, "\ufeff"))
}

func val50(s string) string {
	if len(s) > 50 {
		return s[:50]
	}
	return s
}

func TestExportFilteredUsersAsCSV(t *testing.T) {
	h, fake := app(t)
	r := h.post("/api/users/export", M{"filters": M{"profiles": []int{1}},
		"columns": exportCols, "format": "csv"})
	eq(t, r.code, 200, string(r.raw))
	eq(t, strings.Contains(r.header.Get("Content-Disposition"), `filename="sas4_users_`), true, "filename")
	rows := readExport(t, r)
	eq(t, fmt.Sprint(rows[0]), "[Username Name Status Phone]", "header")
	flat := byProfile(fake, 1)
	eq(t, len(rows)-1, len(flat), "rows")
	eq(t, r.header.Get("X-Row-Count"), fmt.Sprint(len(flat)), "X-Row-Count")
	var first M
	for _, u := range flat {
		if u["username"] == rows[1][0] {
			first = u
		}
	}
	eq(t, rows[1][1], "U "+first["lastname"].(string), "fullname")
	eq(t, rows[1][3], first["phone"].(string), "phone keeps its leading zero")
}

func TestExportOnlyTickedUsersWithAllFields(t *testing.T) {
	h, _ := app(t)
	r := h.post("/api/users/export", M{"filters": M{}, "ids": []int{3, 5},
		"columns": []M{}, "format": "csv"})
	rows := readExport(t, r)
	col := -1
	head := map[string]bool{}
	for i, k := range rows[0] {
		head[k] = true
		if k == "username" {
			col = i
		}
	}
	names := []string{}
	for _, row := range rows[1:] {
		names = append(names, row[col])
	}
	sort.Strings(names)
	eq(t, fmt.Sprint(names), "[user3 user5]", "ticked")
	for _, k := range []string{"remaining_days", "group_name", "user_id"} {
		eq(t, head[k], true, "has "+k)
	}
}

func readXLSX(t *testing.T, raw []byte) [][]string {
	t.Helper()
	book, err := excelize.OpenReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := book.GetRows(book.GetSheetList()[0])
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestExportXLSXOpensAndKeepsPhoneAsText(t *testing.T) {
	h, _ := app(t)
	r := h.post("/api/users/export", M{"filters": M{}, "ids": []int{3},
		"columns": exportCols, "format": "xlsx"})
	eq(t, r.code, 200, string(r.raw))
	book, err := excelize.OpenReader(bytes.NewReader(r.raw))
	if err != nil {
		t.Fatal(err)
	}
	sheet := book.GetSheetList()[0]
	rows, _ := book.GetRows(sheet)
	eq(t, fmt.Sprint(rows[0]), "[Username Name Status Phone]", "header")
	eq(t, rows[1][0], "user3", "username")
	eq(t, rows[1][3], "0900000003", "phone")
	kind, _ := book.GetCellType(sheet, "D2")
	eq(t, kind != excelize.CellTypeNumber, true, "phone is text")
}

func TestExportRefusedWhileLoadingAndProgressReported(t *testing.T) {
	fake := newFake(t)
	fake.delay = 150 * time.Millisecond
	h := loggedIn(t, fake)
	wait(t, 15*time.Second, func() bool { return h.directory()["loaded"].(float64) > 0 })
	status := h.directory()
	eq(t, status["state"], any("loading"), "state")
	eq(t, status["total"], any(150.0), "total")
	eq(t, status["started"] != nil, true, "started")
	eq(t, h.post("/api/users/export", M{"filters": M{}, "format": "csv"}).code, 409, "export")
	fake.set(func() { fake.delay = 0 })
}

// --------------------------------------------------------------------------
// system URL on the login screen
// --------------------------------------------------------------------------

func TestLoginToAURLTypedOnTheLoginScreen(t *testing.T) {
	fake := newFake(t)
	h := newHarness(t, fake, Config{BaseURL: "https://default.example", AllowURLChange: true})
	eq(t, h.get("/api/session").json["default_host"], any("https://default.example"), "default")
	typed := strings.TrimPrefix(fake.URL(), "http://") + "/admin/#/login"
	r := h.post("/api/login", M{"username": "admin", "password": "secret", "url": "http://" + typed})
	eq(t, r.code, 200, string(r.raw))
	eq(t, r.json["host"], any(fake.URL()), "host")
	eq(t, h.get("/api/meta").json["host"], any(fake.URL()), "meta host")
	eq(t, h.get("/api/session").json["host"], any(fake.URL()), "session host")
}

func TestBadOrLockedURLIsRefused(t *testing.T) {
	fake := newFake(t)
	h := newHarness(t, fake, Config{BaseURL: fake.URL(), AllowURLChange: true})
	eq(t, h.post("/api/login", M{"username": "a", "password": "b", "url": "ftp://nope"}).code, 400, "ftp")
	locked := newHarness(t, fake, Config{BaseURL: fake.URL(), AllowURLChange: false})
	eq(t, locked.get("/api/session").json["allow_url_change"], any(false), "allow_url_change")
	eq(t, locked.post("/api/login", M{"username": "admin", "password": "secret",
		"url": "https://other.example"}).code, 403, "other host")
	eq(t, locked.post("/api/login", M{"username": "admin", "password": "secret",
		"url": fake.URL() + "/"}).code, 200, "same host")
}

func TestLoginNeedsAURLWithoutADefault(t *testing.T) {
	fake := newFake(t)
	h := newHarness(t, fake, Config{AllowURLChange: true})
	eq(t, h.get("/api/session").json["default_host"], any(""), "default")
	eq(t, h.post("/api/login", M{"username": "admin", "password": "secret"}).code, 422, "no url")
	eq(t, h.post("/api/login", M{"username": "admin", "password": "secret",
		"url": fake.URL()}).code, 200, "typed url")
}

func TestNormaliseURL(t *testing.T) {
	for in, want := range map[string]string{
		"sas.example.com":                 "https://sas.example.com",
		"https://sas.example.com/#/login": "https://sas.example.com",
		"http://10.0.0.5:8080/admin/":     "http://10.0.0.5:8080",
		"HTTP://Example.COM":              "http://example.com",
		"http://[::1]:9000/x":             "http://[::1]:9000",
	} {
		got, err := NormaliseURL(in)
		eq(t, err, nil, in)
		eq(t, got, want, in)
	}
	for _, bad := range []string{"", "ftp://x", "http://user@host", "http://host:abc", "http://"} {
		if _, err := NormaliseURL(bad); err == nil {
			t.Fatalf("%q should be refused", bad)
		}
	}
}

// --------------------------------------------------------------------------
// reports
// --------------------------------------------------------------------------

func TestReportCountsNewUsersPerDayAndMonth(t *testing.T) {
	h, fake := app(t)
	r := h.post("/api/reports", M{"period": "day", "date_from": "2026-09-01", "date_to": "2026-09-28"})
	eq(t, r.code, 200, string(r.raw))
	rows := r.json["rows"].([]any)
	eq(t, len(rows), 28, "days")
	byDay := map[string]int{}
	fake.set(func() {
		for _, u := range fake.users {
			byDay[u["created_at"].(string)[:10]]++
		}
	})
	got := map[string]int{}
	for _, row := range rows {
		m := row.(M)
		got[m["start"].(string)] = int(m["new"].(float64))
	}
	eq(t, fmt.Sprint(got), fmt.Sprint(byDay), "new per day")
	totals := r.json["totals"].(M)
	eq(t, totals["new"], any(150.0), "new total")
	// Expirations in the fake are 2020 or 2099: none fall in September.
	eq(t, totals["expired"], any(0.0), "expired")

	month := h.post("/api/reports", M{"period": "month", "date_from": "2026-09-01",
		"date_to": "2026-09-30", "profiles": []int{1}}).json
	mrows := month["rows"].([]any)
	eq(t, len(mrows), 1, "months")
	eq(t, mrows[0].(M)["period"], any("2026-09"), "period")
	eq(t, int(mrows[0].(M)["new"].(float64)), len(byProfile(fake, 1)), "month new")
}

func TestReportActiveCountsOnlyEnabledUnexpiredUsers(t *testing.T) {
	h, fake := app(t)
	report := h.post("/api/reports", M{"period": "week"}).json
	expected := 0
	fake.set(func() {
		for _, u := range fake.users {
			if u["enabled"] == 1 && strings.HasPrefix(u["expiration"].(string), "2099") {
				expected++
			}
		}
	})
	eq(t, int(report["totals"].(M)["active_now"].(float64)), expected, "active_now")
	rows := report["rows"].([]any)
	eq(t, int(rows[len(rows)-1].(M)["active"].(float64)), expected, "last active")
}

func TestReportDownloadAndValidation(t *testing.T) {
	h, _ := app(t)
	r := h.post("/api/reports", M{"period": "month", "format": "csv",
		"date_from": "2026-01-01", "date_to": "2026-12-31"})
	eq(t, r.code, 200, "csv")
	rows := readExport(t, r)
	eq(t, fmt.Sprint(rows[0][:4]), "[Period From To Active at end]", "header")
	eq(t, len(rows), 13, "rows")
	r = h.post("/api/reports", M{"period": "month", "format": "xlsx"})
	eq(t, r.code, 200, "xlsx")
	eq(t, string(r.raw[:2]), "PK", "zip")
	readXLSX(t, r.raw)
	eq(t, h.post("/api/reports", M{"period": "year"}).code, 400, "year")
	eq(t, h.post("/api/reports", M{"date_from": "2026-09-10", "date_to": "2026-09-01"}).code, 400, "reversed")
	eq(t, h.post("/api/reports", M{"period": "day", "date_from": "2020-01-01",
		"date_to": "2026-01-01"}).code, 400, "too many days")
}

// --------------------------------------------------------------------------
// last session report
// --------------------------------------------------------------------------

func (h *harness) scanSessions(lookback int) M {
	h.t.Helper()
	r := h.post("/api/sessions/refresh", M{"lookback_days": lookback})
	eq(h.t, r.code, 200, string(r.raw))
	wait(h.t, 15*time.Second, func() bool { return h.get("/api/sessions/status").json["state"] == "ready" })
	return h.get("/api/sessions/status").json
}

func (h *harness) sessionRows(body M) M {
	h.t.Helper()
	if body == nil {
		body = M{}
	}
	body["page_size"] = 500
	r := h.post("/api/sessions/query", body)
	eq(h.t, r.code, 200, string(r.raw))
	return r.json
}

func rowsByUsername(result M) map[string]M {
	out := map[string]M{}
	for _, r := range result["rows"].([]any) {
		m := r.(M)
		out[m["username"].(string)] = m
	}
	return out
}

func TestLastSessionKeepsTheNewestSessionPerUser(t *testing.T) {
	h, fake := app(t)
	status := h.scanSessions(90)
	rows := rowsByUsername(h.sessionRows(nil))
	eq(t, len(rows), 150, "rows")
	newest := ""
	fake.set(func() {
		for _, s := range fake.sessions {
			if s["username"] == "user3" && s["acctstarttime"].(string) > newest {
				newest = s["acctstarttime"].(string)
			}
		}
	})
	user3 := rows["user3"]
	eq(t, user3["start"], any(newest), "start")
	eq(t, user3["ip"], any("10.0.3.1"), "ip")
	eq(t, user3["mac"], any("AA:BB:CC:00:03:00"), "mac")
	eq(t, user3["upload"], any(3000.0), "upload")
	eq(t, user3["download"], any(15000.0), "download")
	eq(t, user3["total"], any(18000.0), "total")
	eq(t, user3["duration_s"], any(3600.0), "duration")
	eq(t, user3["state"], any("offline"), "state")
	eq(t, user3["terminate_cause"] != "", true, "cause")
	// Online: the newest session has no stop time.
	eq(t, rows["user4"]["state"], any("online"), "user4")
	eq(t, rows["user4"]["stop"], any(""), "user4 stop")
	eq(t, rows["user6"]["state"], any("none"), "user6") // no sessions at all
	eq(t, status["progress"].(M)["pages"].(float64) >= 1, true, "pages")
	// Newest first, users without a session last.
	starts := []string{}
	for _, r := range h.sessionRows(nil)["rows"].([]any) {
		s, _ := r.(M)["start"].(string)
		starts = append(starts, s)
	}
	eq(t, sort.IsSorted(sort.Reverse(sort.StringSlice(starts))), true, "sorted")
}

func TestScanStopsOnceTheLookBackIsReached(t *testing.T) {
	h, _ := app(t)
	status := h.scanSessions(30)
	eq(t, strings.Contains(status["stopped"].(string), "older than 30 days"), true, "stopped")
	rows := rowsByUsername(h.sessionRows(nil))
	eq(t, rows["user9"]["state"], any("none"), "user9 in 30 days")
	full := h.scanSessions(0)
	eq(t, full["stopped"], any("end of the session history"), "full stopped")
	rows = rowsByUsername(h.sessionRows(nil))
	eq(t, rows["user9"]["state"], any("offline"), "user9 in full")
}

func TestScanStopsEarlyWhenEveryUserIsFound(t *testing.T) {
	fake := newFake(t)
	for i := range fake.users { // everyone has a session
		if i%6 == 0 || i%9 == 0 {
			delete(fake.users, i)
		}
	}
	kept := []M{}
	for _, s := range fake.sessions {
		for _, u := range fake.users {
			if u["username"] == s["username"] {
				kept = append(kept, s)
				break
			}
		}
	}
	fake.sessions = kept
	h := loggedIn(t, fake)
	h.waitReady()
	status := h.scanSessions(0)
	eq(t, status["stopped"], any("every user found"), "stopped")
	// 40 a page: every user's newest session is near the top, so the older
	// pages are never requested.
	totalPages := (len(fake.sessions) + 39) / 40
	eq(t, len(fake.callsTo("index/UserSessions")) < totalPages, true, "pages read")
}

func TestSessionFiltersAndStates(t *testing.T) {
	h, fake := app(t)
	h.scanSessions(90)
	online := h.sessionRows(M{"states": []string{"online"}})
	eq(t, online["totals"].(M)["users"], online["total"], "totals")
	for _, r := range online["rows"].([]any) {
		eq(t, r.(M)["state"], any("online"), "state")
	}
	byProf := h.sessionRows(M{"filters": M{"profiles": []int{1}}})
	eq(t, int(byProf["total"].(float64)), len(byProfile(fake, 1)), "profile")
	tt := h.sessionRows(nil)["totals"].(M)
	eq(t, int(tt["online"].(float64)+tt["offline"].(float64)+tt["none"].(float64)), 150, "sum")
}

func TestSessionHistoryIsExactUsernameOnly(t *testing.T) {
	h, _ := app(t)
	r := h.post("/api/sessions/history", M{"username": "user1"})
	eq(t, r.code, 200, string(r.raw))
	rows := r.json["rows"].([]any)
	// "user1" is a substring of user10..user19 and user100..: never shown.
	starts := []string{}
	for _, row := range rows {
		eq(t, row.(M)["username"], any("user1"), "username")
		starts = append(starts, row.(M)["start"].(string))
	}
	eq(t, len(rows), 3, "rows")
	eq(t, sort.IsSorted(sort.Reverse(sort.StringSlice(starts))), true, "sorted")
	eq(t, r.json["user"].(M)["profile_name"] != nil, true, "profile_name")
	eq(t, r.json["next"], nil, "next") // all of it fit on one page
}

func TestSessionExportCSVAndXLSX(t *testing.T) {
	h, _ := app(t)
	h.scanSessions(90)
	r := h.post("/api/sessions/query", M{"format": "csv", "states": []string{"offline"}})
	eq(t, r.code, 200, string(r.raw))
	rows := readExport(t, r)
	head := rows[0]
	eq(t, fmt.Sprint(head[:3]), "[Username Name Profile]", "header")
	status := -1
	have := map[string]bool{}
	for i, k := range head {
		have[k] = true
		if k == "Status" {
			status = i
		}
	}
	for _, k := range []string{"IP address", "MAC", "Upload", "Download", "Duration"} {
		eq(t, have[k], true, "has "+k)
	}
	for _, row := range rows[1:] {
		eq(t, row[status], "offline", "status")
	}
	r = h.post("/api/sessions/query", M{"format": "xlsx"})
	eq(t, r.code, 200, "xlsx")
	eq(t, string(r.raw[:2]), "PK", "zip")
	eq(t, readXLSX(t, r.raw)[0][0], "Username", "first cell")
}

func TestSessionsNeedAScanAndALoadedList(t *testing.T) {
	fake := newFake(t)
	fake.delay = 150 * time.Millisecond
	h := loggedIn(t, fake)
	eq(t, h.post("/api/sessions/refresh", M{"lookback_days": 90}).code, 409, "while loading")
	fake.set(func() { fake.delay = 0 })
	h.waitReady()
	eq(t, h.post("/api/sessions/query", M{}).code, 409, "before a scan")
	eq(t, h.post("/api/sessions/refresh", M{"lookback_days": 7}).code, 400, "bad look-back")
}

// --------------------------------------------------------------------------
// CSV reading
// --------------------------------------------------------------------------

func TestReadCSVBytes(t *testing.T) {
	rows := ReadCSVBytes([]byte("\ufeffUserName;Amount\nali;5\n;\n"))
	eq(t, fmt.Sprint(rows), "[map[amount:5 username:ali]]", "semicolons, BOM, blank row")
	rows = ReadCSVBytes([]byte("ali\nomar\n"))
	eq(t, fmt.Sprint(rows), "[map[username:ali] map[username:omar]]", "headerless column")
	// cp1256: "علي" as Windows Arabic bytes
	rows = ReadCSVBytes([]byte("username\n\xda\xe1\xed\n"))
	eq(t, rows[0]["username"], "علي", "cp1256")
}

// --------------------------------------------------------------------------
// desktop window
// --------------------------------------------------------------------------

type saved struct {
	name string
	data []byte
}

// desktopApp is the app as the desktop window runs it, already past the
// ?k= handshake and logged in.
func desktopApp(t *testing.T, cancel bool) (*harness, *[]saved) {
	fake := newFake(t)
	files := &[]saved{}
	h := newHarness(t, fake, Config{BaseURL: fake.URL(), AllowURLChange: true,
		Desktop: true, DesktopSecret: "s3cret",
		SaveFile: func(name string, data []byte) (string, error) {
			*files = append(*files, saved{name, data})
			if cancel {
				return "", nil
			}
			return "/picked/" + name, nil
		}})
	r := h.get("/?k=s3cret")
	eq(t, r.code, 200, "handshake")
	eq(t, strings.Contains(string(r.raw), `location.replace("/")`), true, "handshake moves on by script")
	eq(t, h.get("/").code, 200, "page with the cookie")
	h.login()
	h.waitReady()
	return h, files
}

func (h *harness) postSave(path string, body any) resp {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", h.base+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Save-Dialog", "1")
	return h.do(req)
}

func TestDesktopNeedsTheWindowSecret(t *testing.T) {
	fake := newFake(t)
	h := newHarness(t, fake, Config{BaseURL: fake.URL(), Desktop: true, DesktopSecret: "s3cret"})
	eq(t, h.get("/").code, 403, "no cookie")
	eq(t, h.get("/api/session").code, 403, "api without cookie")
	eq(t, h.get("/?k=wrong").code, 403, "wrong secret")
	r := h.get("/?k=s3cret")
	eq(t, r.code, 200, "handshake")
	// A 302 would lose the SameSite=Strict cookie after the cross-site
	// arrival from Wails' scheme; the page must move itself on instead.
	eq(t, strings.Contains(string(r.raw), `location.replace("/")`), true, "no redirect")
	eq(t, strings.Contains(r.header.Get("Set-Cookie"), "SameSite=Strict"), true, "strict cookie")
	r = h.get("/api/session")
	eq(t, r.code, 200, "api with cookie")
	eq(t, r.json["desktop"], any(true), "desktop flag")
}

func TestDesktopDownloadsGoThroughTheSaveDialog(t *testing.T) {
	h, files := desktopApp(t, false)
	r := h.postSave("/api/users/export", M{"filters": M{}, "ids": []int{3}, "columns": exportCols, "format": "csv"})
	eq(t, r.code, 200, string(r.raw))
	eq(t, strings.HasPrefix(r.str("saved"), "/picked/sas4_users_1_"), true, r.str("saved"))
	f := (*files)[0]
	eq(t, strings.HasSuffix(f.name, ".csv"), true, f.name)
	rows := csvRows(t, strings.TrimPrefix(string(f.data), "\ufeff"))
	eq(t, rows[1][0], "user3", "csv content")

	r = h.postSave("/api/reports", M{"period": "month", "format": "xlsx"})
	eq(t, r.code, 200, string(r.raw))
	eq(t, string((*files)[1].data[:2]), "PK", "xlsx content")

	h.scanSessions(90)
	r = h.postSave("/api/sessions/query", M{"format": "csv"})
	eq(t, r.code, 200, string(r.raw))
	eq(t, strings.HasPrefix((*files)[2].name, "sas4_last_sessions_"), true, (*files)[2].name)

	job := h.runJob(M{"op": "delete", "dry_run": true, "params": M{},
		"source": M{"type": "filter", "filters": M{}, "ids": []int{4}}})
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/jobs/%d/results.csv", h.base, jsonID(job["id"])), nil)
	req.Header.Set("X-Save-Dialog", "1")
	r = h.do(req)
	eq(t, r.code, 200, string(r.raw))
	eq(t, strings.Contains(string((*files)[3].data), "would_run"), true, "job report content")

	// Without the header the file still comes back as a download.
	r = h.post("/api/reports", M{"period": "month", "format": "csv"})
	eq(t, strings.HasPrefix(string(r.raw), "\ufeff"), true, "plain download")
	eq(t, len(*files), 4, "no dialog without the header")
}

func TestDesktopSaveCancelled(t *testing.T) {
	h, _ := desktopApp(t, true)
	r := h.postSave("/api/reports", M{"period": "month", "format": "csv"})
	eq(t, r.code, 200, string(r.raw))
	eq(t, r.json["cancelled"], any(true), "cancelled")
}
