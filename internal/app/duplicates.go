package app

// Users who share an address with another user: the same MAC or IP on their
// last session, or the same static IP on their record. SAS4 has no filter
// for this, so it is worked out here over the cached lists.
//
// Duplicates are found across the WHOLE user list and only then narrowed by
// the other filters: "profile X + shared MAC" is the users of X whose MAC
// anyone else also has.
//
// Session IPs are compared between online sessions only. A pool address is
// handed to someone else after a logout, so comparing offline last sessions
// would flag nearly everyone.

import (
	"sort"
	"strings"

	"sastoolkit/internal/val"
)

var DuplicateKinds = []string{"session_mac", "session_ip", "static_ip"}

// DuplicateColumns are the fields ApplyDuplicates adds to each row; the
// session ones are also added by ApplySessionFilters.
var DuplicateColumns = []string{"dup_value", "dup_count", "session_mac",
	"session_ip", "session_state", "session_start", "nas_ip",
	"session_upload", "session_download", "session_total",
	"session_stop_reason"}

// sessionFields: a last session (flattenSession) as row fields.
func sessionFields(s M) M {
	return M{"session_mac": s["mac"], "session_ip": s["ip"],
		"session_state": s["state"], "session_start": s["start"],
		"nas_ip": s["nas_ip"], "session_upload": s["upload_h"],
		"session_download": s["download_h"], "session_total": s["total_h"],
		"session_stop_reason": s["terminate_cause"]}
}

// cleanKinds keeps the known kinds, in DuplicateKinds order.
func cleanKinds(kinds []string) []string {
	want := map[string]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	out := []string{}
	for _, k := range DuplicateKinds {
		if want[k] {
			out = append(out, k)
		}
	}
	return out
}

// needsSessions: whether any of the kinds is read from the session scan.
func needsSessions(kinds []string) bool {
	for _, k := range kinds {
		if k == "session_mac" || k == "session_ip" {
			return true
		}
	}
	return false
}

func normMAC(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	return strings.NewReplacer(":", "", "-", "", ".", "", " ", "").Replace(s)
}

func normIP(s string) string {
	s = strings.TrimSpace(s)
	if s == "0.0.0.0" || s == "::" {
		return ""
	}
	return s
}

// dupValue is the value a user is compared on, or "" to leave them out.
func dupValue(user M, last map[string]M, kind string) string {
	if kind == "static_ip" {
		return normIP(val.Str(user["static_ip"]))
	}
	session := last[val.Fold(strings.TrimSpace(val.Str(user["username"])))]
	if session == nil {
		return ""
	}
	switch kind {
	case "session_mac":
		return normMAC(val.Str(session["mac"]))
	case "session_ip":
		if !val.Truthy(session["online"]) {
			return ""
		}
		return normIP(val.Str(session["ip"]))
	}
	return ""
}

var dupLabels = map[string]string{"session_mac": "MAC", "session_ip": "IP",
	"static_ip": "Static IP"}

// FindDuplicates -> casefolded username -> the fields to add to that user's
// row. With several kinds, a user is in if any of them is shared; the first
// kind (in DuplicateKinds order) that hits names their group.
func FindDuplicates(all []M, last map[string]M, kinds []string) map[string]M {
	kinds = cleanKinds(kinds)
	out := map[string]M{}
	for _, kind := range kinds {
		groups := map[string][]string{}
		for _, user := range all {
			v := dupValue(user, last, kind)
			name := val.Fold(strings.TrimSpace(val.Str(user["username"])))
			if v == "" || name == "" {
				continue
			}
			groups[v] = append(groups[v], name)
		}
		for v, names := range groups {
			if len(names) < 2 {
				continue
			}
			for _, name := range names {
				if out[name] != nil {
					continue
				}
				extra := M{"dup_value": dupLabels[kind] + " " + v,
					"dup_count": len(names)}
				if s := last[name]; s != nil {
					for k, v := range sessionFields(s) {
						extra[k] = v
					}
				}
				out[name] = extra
			}
		}
	}
	return out
}

// ApplyDuplicates keeps the rows in dups, as copies with the duplicate
// fields added, grouped by the shared value.
func ApplyDuplicates(rows []M, dups map[string]M) []M {
	out := []M{}
	for _, user := range rows {
		extra := dups[val.Fold(strings.TrimSpace(val.Str(user["username"])))]
		if extra == nil {
			continue
		}
		row := val.Copy(user)
		for k, v := range extra {
			if !val.Blank(v) {
				row[k] = v
			}
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := val.Str(out[i]["dup_value"]), val.Str(out[j]["dup_value"])
		if a != b {
			return a < b
		}
		return val.Fold(val.Str(out[i]["username"])) <
			val.Fold(val.Str(out[j]["username"]))
	})
	return out
}
