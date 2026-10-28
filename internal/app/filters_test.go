package app

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// dupApp: a fake where
//
//   - user1, user2, user3 share a MAC on their newest session, written three
//     ways; user20 has it only on an older session, so it does not count;
//   - user4 and user8 (online) share an IP; user5 has it too but is offline;
//   - user10 and user11 share a static IP; user13's is unique;
//   - user5's last session ended on NAS 10.9.9.9 with Lost-Carrier (every
//     other NAS is 10.255.0.1, every other offline stop User-Request).
//
// The fake's newest session of user i uploads 1000*i bytes and downloads
// 5000*i.
func dupApp(t *testing.T) (*harness, *FakeSAS4) {
	fake := newFake(t)
	mac := map[string]string{"user1": "AA:BB:CC:DD:EE:FF",
		"user2": "aa-bb-cc-dd-ee-ff", "user3": "aabb.ccdd.eeff"}
	newest := map[string]bool{}
	for _, s := range fake.sessions { // newest first per user: k == 0 comes first
		name := s["username"].(string)
		first := !newest[name]
		newest[name] = true
		if m, ok := mac[name]; ok && first {
			s["callingstationid"] = m
		}
		if name == "user20" && !first {
			s["callingstationid"] = "AA:BB:CC:DD:EE:FF"
		}
		if (name == "user4" || name == "user8" || name == "user5") && first {
			s["framedipaddress"] = "100.64.0.9"
		}
		if name == "user5" && first {
			s["nasipaddress"] = "10.9.9.9"
			s["acctterminatecause"] = "Lost-Carrier"
		}
	}
	fake.users[10]["static_ip"] = "192.0.2.5"
	fake.users[11]["static_ip"] = "192.0.2.5"
	fake.users[13]["static_ip"] = "192.0.2.6"
	h := newHarness(t, fake, Config{BaseURL: fake.URL(), AllowURLChange: true})
	h.login()
	h.waitReady()
	return h, fake
}

func dupNames(t *testing.T, r resp) []string {
	t.Helper()
	eq(t, r.code, 200, string(r.raw))
	out := []string{}
	for _, row := range r.json["rows"].([]any) {
		out = append(out, row.(M)["username"].(string))
	}
	sort.Strings(out)
	return out
}

func dupQuery(h *harness, filters M) resp {
	return h.post("/api/users/query", M{"filters": filters, "page_size": 500})
}

func TestDuplicateFiltersOnTheUserList(t *testing.T) {
	h, _ := dupApp(t)

	r := dupQuery(h, M{"duplicates": []string{"session_mac"}})
	eq(t, r.code, 409, "before a scan")
	eq(t, strings.Contains(r.str("detail"), "scan the sessions"), true, r.str("detail"))

	got := dupNames(t, dupQuery(h, M{"duplicates": []string{"static_ip"}}))
	eq(t, fmt.Sprint(got), "[user10 user11]", "static ip, no scan needed")

	h.scanSessions(90)
	r = dupQuery(h, M{"duplicates": []string{"session_mac"}})
	eq(t, fmt.Sprint(dupNames(t, r)), "[user1 user2 user3]", "session mac")
	first := r.json["rows"].([]any)[0].(M)
	eq(t, first["dup_count"], any(3.0), "dup_count")
	eq(t, first["dup_value"], any("MAC AABBCCDDEEFF"), "dup_value")
	eq(t, first["session_state"] != nil, true, "session fields")

	got = dupNames(t, dupQuery(h, M{"duplicates": []string{"session_ip"}}))
	eq(t, fmt.Sprint(got), "[user4 user8]", "session ip, online only")

	got = dupNames(t, dupQuery(h, M{"duplicates": []string{"session_mac"},
		"profiles": []int{1}}))
	eq(t, fmt.Sprint(got), "[user3]", "narrowed by profile")

	got = dupNames(t, dupQuery(h, M{"duplicates": []string{"session_mac", "static_ip"}}))
	eq(t, fmt.Sprint(got), "[user1 user10 user11 user2 user3]", "any kind")

	// A per-user job runs on the same users.
	job := h.runJob(M{"op": "delete", "dry_run": true, "source": M{"type": "filter",
		"filters": M{"duplicates": []string{"session_mac"}}}})
	eqCounts(t, job, map[string]int{"would_run": 3})
}

func TestBulkChangeWithDuplicatesRunsPerUser(t *testing.T) {
	h, fake := dupApp(t)
	dups := []string{"session_mac"}

	r := h.post("/api/bulk/preview", M{"filters": []M{}, "extra": M{"duplicates": dups}})
	eq(t, r.code, 409, "before a scan")
	h.scanSessions(90)

	got := dupNames(t, h.post("/api/bulk/preview", M{"filters": []M{}, "extra": M{"duplicates": dups}}))
	eq(t, fmt.Sprint(got), "[user1 user2 user3]", "preview, no conditions")
	r = h.post("/api/bulk/preview", M{"filters": []M{}, "extra": M{"duplicates": dups}})
	eq(t, r.json["mode"], any("each"), "mode")
	eq(t, r.json["rows"].([]any)[0].(M)["session_mac"] != nil, true, "session shown")

	got = dupNames(t, h.post("/api/bulk/preview", M{"filters": flatFilter, "extra": M{"duplicates": dups}}))
	eq(t, fmt.Sprint(got), "[user3]", "preview with a condition")

	res := h.post("/api/bulk/apply", M{"filters": []M{}, "extra": M{"duplicates": dups},
		"action": "disable", "expected_count": 4})
	eq(t, res.code, 400, "count changed")

	res = h.post("/api/bulk/apply", M{"filters": []M{}, "extra": M{"duplicates": dups},
		"action": "disable", "expected_count": 3, "dry_run": true})
	eq(t, res.code, 200, string(res.raw))
	job := h.waitJob(jsonID(res.json["id"]), "done")
	eqCounts(t, job, map[string]int{"would_run": 3})
	fake.set(func() { eq(t, len(fake.bulkApplied), 0, "dry run sends nothing") })

	res = h.post("/api/bulk/apply", M{"filters": []M{}, "extra": M{"duplicates": dups},
		"action": "disable", "expected_count": 3})
	eq(t, res.code, 200, string(res.raw))
	job = h.waitJob(jsonID(res.json["id"]), "done")
	eqCounts(t, job, map[string]int{"done": 3})
	fake.set(func() {
		eq(t, len(fake.bulkApplied), 3, "one request per user")
		sent := []string{}
		for _, a := range fake.bulkApplied {
			eq(t, a.matched, 1, "matched")
			f := a.body["filter"].([]any)
			eq(t, len(f), 1, "one condition")
			c := f[0].(map[string]any)
			eq(t, c["key"], any("username"), "key")
			eq(t, c["op"], any("="), "op")
			sent = append(sent, c["value"].(string))
		}
		sort.Strings(sent)
		eq(t, fmt.Sprint(sent), "[user1 user2 user3]", "users")
		for _, i := range []int{1, 2, 3} {
			eq(t, fmt.Sprint(fake.users[i]["enabled"]), "0", "disabled")
		}
	})
}

func TestBulkChangePerUserSkipsWhenTheUsernameFilterIsNotExact(t *testing.T) {
	h, fake := dupApp(t)
	got := dupNames(t, h.post("/api/bulk/preview", M{"filters": []M{},
		"extra": M{"duplicates": []string{"static_ip"}}}))
	eq(t, fmt.Sprint(got), "[user10 user11]", "preview")
	fake.set(func() { fake.ignoreBulkFilter = true })
	res := h.post("/api/bulk/apply", M{"filters": []M{}, "extra": M{"duplicates": []string{"static_ip"}},
		"action": "disable", "expected_count": 2})
	eq(t, res.code, 200, string(res.raw))
	job := h.waitJob(jsonID(res.json["id"]), "done")
	eqCounts(t, job, map[string]int{"skipped": 2})
	fake.set(func() { eq(t, len(fake.bulkApplied), 0, "nothing sent") })
}

func TestSessionFiltersOnTheUserList(t *testing.T) {
	h, _ := dupApp(t)
	r := dupQuery(h, M{"mac": "bbccdd"})
	eq(t, r.code, 409, "before a scan")
	eq(t, dupQuery(h, M{"profiles": []int{1}}).code, 200, "no session filter, no scan needed")

	status := h.scanSessions(90)
	eq(t, fmt.Sprint(status["stop_reasons"]), "[Lost-Carrier User-Request]", "stop reasons")

	cases := []struct {
		what    string
		filters M
		want    string
	}{
		{"mac contains, separators ignored", M{"mac": "bb:cc:dd"}, "[user1 user2 user3]"},
		{"nas ip", M{"nas_ip": "10.9"}, "[user5]"},
		{"stop reason", M{"stop_reasons": []string{"lost-carrier"}}, "[user5]"},
		{"upload range", M{"upload_min": 140000, "upload_max": 143000},
			"[user140 user141 user142 user143]"},
		{"total max", M{"total_max": 12000}, "[user1 user2]"},
		// profile 2 is i%3 == 1, online is i%4 == 0: i = 4, 16, 28 up to 30.
		{"state, profile and total", M{"session_states": []string{"online"},
			"profiles": []int{2}, "total_max": 6000 * 30}, "[user16 user28 user4]"},
	}
	for _, c := range cases {
		got := dupNames(t, dupQuery(h, c.filters))
		eq(t, fmt.Sprint(got), c.want, c.what)
	}

	// Session fields ride along with the rows.
	row := dupQuery(h, M{"nas_ip": "10.9"}).json["rows"].([]any)[0].(M)
	eq(t, row["nas_ip"], any("10.9.9.9"), "nas_ip")
	eq(t, row["session_stop_reason"], any("Lost-Carrier"), "stop reason field")

	// "none": every 6th user has no session, every 9th only ~200 days ago.
	none := dupNames(t, dupQuery(h, M{"session_states": []string{"none"}}))
	eq(t, len(none), 33, "no session in the window")
	old := h.sessionRows(M{"states": []string{"none"}})
	eq(t, old["total"], any(33.0), "older states field")
	sameAsNew := h.sessionRows(M{"filters": M{"session_states": []string{"none"}}})
	eq(t, sameAsNew["total"], any(33.0), "session_states on the report")
}

func TestReportsTakeTheSameFilters(t *testing.T) {
	h, _ := dupApp(t)
	body := func(filters M) M {
		return M{"period": "month", "date_from": "2026-09-01", "date_to": "2026-09-30",
			"filters": filters}
	}
	users := func(r resp) any {
		eq(t, r.code, 200, string(r.raw))
		return r.json["totals"].(M)["users"]
	}
	eq(t, users(h.post("/api/reports", body(M{}))), any(150.0), "everyone")
	eq(t, users(h.post("/api/reports", body(M{"duplicates": []string{"static_ip"}}))),
		any(2.0), "shared static ip")
	eq(t, h.post("/api/reports", body(M{"mac": "bbccdd"})).code, 409, "before a scan")
	h.scanSessions(90)
	eq(t, users(h.post("/api/reports", body(M{"mac": "bbccdd"}))), any(3.0), "mac")
	eq(t, users(h.post("/api/reports", body(M{"mac": "bbccdd", "profiles": []int{1}}))),
		any(1.0), "mac and profile")
}

func TestBulkChangeWithSessionFilterRunsPerUser(t *testing.T) {
	h, fake := dupApp(t)
	h.scanSessions(90)
	extra := M{"mac": "aabbccddeeff"}
	r := h.post("/api/bulk/preview", M{"filters": []M{}, "extra": extra})
	eq(t, fmt.Sprint(dupNames(t, r)), "[user1 user2 user3]", "preview")
	eq(t, r.json["rows"].([]any)[0].(M)["session_mac"] != nil, true, "session shown")
	res := h.post("/api/bulk/apply", M{"filters": []M{}, "extra": extra,
		"action": "disable", "expected_count": 3})
	eq(t, res.code, 200, string(res.raw))
	eqCounts(t, h.waitJob(jsonID(res.json["id"]), "done"), map[string]int{"done": 3})
	fake.set(func() { eq(t, len(fake.bulkApplied), 3, "one request per user") })

	// Nothing set in extra: the usual single SAS4 request.
	r = h.post("/api/bulk/preview", M{"filters": flatFilter, "extra": M{"mac": " "}})
	eq(t, r.code, 200, string(r.raw))
	eq(t, r.json["mode"], nil, "server-side mode")
	eq(t, r.json["sample"] != nil, true, "sample")
}
