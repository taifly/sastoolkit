package app

// Filters on each user's last session, from the LastSessions scan: its
// state, MAC, NAS IP, stop reason and traffic. A user with no session in the
// look-back window only passes the state filter "none"; every other session
// filter leaves them out.

import (
	"strings"

	"sastoolkit/internal/val"
)

type trafficBound struct {
	field    string
	min, max *float64
}

func trafficBounds(f UserFilters) []trafficBound {
	out := []trafficBound{}
	for _, b := range []trafficBound{{"upload", f.UploadMin, f.UploadMax},
		{"download", f.DownloadMin, f.DownloadMax},
		{"total", f.TotalMin, f.TotalMax}} {
		if b.min != nil || b.max != nil {
			out = append(out, b)
		}
	}
	return out
}

// sessionFiltered: whether any of the last-session filters is set.
func sessionFiltered(f UserFilters) bool {
	return strings.TrimSpace(f.MAC) != "" || strings.TrimSpace(f.NasIP) != "" ||
		len(f.StopReasons) > 0 || len(cleanStates(f.SessionStates)) > 0 ||
		len(trafficBounds(f)) > 0
}

// needsScan: whether the filters read the session scan at all.
func needsScan(f UserFilters) bool {
	return sessionFiltered(f) || needsSessions(cleanKinds(f.Duplicates))
}

func cleanStates(states []string) map[string]bool {
	out := map[string]bool{}
	for _, s := range states {
		for _, known := range sessionStates {
			if s == known {
				out[s] = true
			}
		}
	}
	return out
}

// ApplySessionFilters keeps the rows whose last session passes f, as copies
// with the session's fields added (sessionFields).
func ApplySessionFilters(rows []M, last map[string]M, f UserFilters) []M {
	if !sessionFiltered(f) {
		return rows
	}
	states := cleanStates(f.SessionStates)
	mac := normMAC(f.MAC)
	nas := strings.TrimSpace(f.NasIP)
	reasons := map[string]bool{}
	for _, r := range f.StopReasons {
		reasons[val.Fold(strings.TrimSpace(r))] = true
	}
	bounds := trafficBounds(f)
	out := []M{}
	for _, user := range rows {
		session := last[val.Fold(strings.TrimSpace(val.Str(user["username"])))]
		if session == nil {
			if states["none"] && mac == "" && nas == "" && len(reasons) == 0 &&
				len(bounds) == 0 {
				out = append(out, user)
			}
			continue
		}
		if len(states) > 0 && !states[val.Str(session["state"])] {
			continue
		}
		if mac != "" && !strings.Contains(normMAC(val.Str(session["mac"])), mac) {
			continue
		}
		if nas != "" && !strings.Contains(val.Str(session["nas_ip"]), nas) {
			continue
		}
		if len(reasons) > 0 &&
			!reasons[val.Fold(strings.TrimSpace(val.Str(session["terminate_cause"])))] {
			continue
		}
		inRange := true
		for _, b := range bounds {
			n := float64(octets(session[b.field]))
			if (b.min != nil && n < *b.min) || (b.max != nil && n > *b.max) {
				inRange = false
				break
			}
		}
		if inRange {
			if _, has := user["session_state"]; !has { // a copy already
				user = val.Copy(user)
				for k, v := range sessionFields(session) {
					if !val.Blank(v) {
						user[k] = v
					}
				}
			}
			out = append(out, user)
		}
	}
	return out
}
