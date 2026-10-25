package app

// Active / new / expired users per day, week or month, from the loaded user
// list.
//
//	new       created_at falls in the period
//	expired   expiration falls in the period and is already in the past
//	expiring  expiration falls in the period and is still ahead (future
//	          periods, or the rest of the current one)
//	active    at the END of the period: enabled, created on or before that
//	          moment (or no creation date), and expiring after it
//
// SAS4 only tells us each user's CURRENT expiration, not its history. A user
// who lapsed in March and renewed in May now carries a May-based expiration,
// so the March "active" figure counts them as active. Past `active` values
// are therefore an estimate built from today's data, increasingly so the
// further back they go; `new` and `expired` are exact for the data as it
// stands. The disabled flag is likewise today's: disabled users are never
// counted active.

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"sort"
	"strings"
	"time"

	"sastoolkit/internal/val"
)

var periods = []string{"day", "week", "month"}

const maxPeriods = 1100 // ~3 years of days

var ReportColumns = []Column{{"period", "Period"}, {"start", "From"},
	{"end", "To"}, {"active", "Active at end"}, {"new", "New"},
	{"expired", "Expired"}, {"expiring", "Expiring"}}

type ReportError struct{ Msg string }

func (e *ReportError) Error() string { return e.Msg }

func reportDT(v any) (time.Time, bool) {
	text := strings.TrimSpace(val.Str(v))
	if text == "" {
		return time.Time{}, false
	}
	head := val.Cut(text, 19)
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05",
		"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, head, time.Local); err == nil {
			return t, true
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", val.Cut(text, 10), time.Local); err == nil {
		return t, true
	}
	return time.Time{}, false
}

type reportUser struct {
	profile    int
	hasProfile bool
	manager    string
	enabled    bool
	created    time.Time
	hasCreated bool
	expires    time.Time
	hasExpires bool
}

// parsedUsers is parsed once per loaded list -- the dates are the slow part,
// and the same list is reported on again and again with different ranges and
// filters.
func (d *Directory) parsedUsers() []reportUser {
	d.mu.RLock()
	if d.reportCacheGenerate == d.generation && d.reportCache != nil {
		defer d.mu.RUnlock()
		return d.reportCache
	}
	users, generation := d.users, d.generation
	d.mu.RUnlock()
	rows := make([]reportUser, 0, len(users))
	for _, u := range users {
		var r reportUser
		r.profile, r.hasProfile = val.Int(u["profile_id"])
		r.manager = val.Fold(val.Str(u["parent_username"]))
		r.enabled = val.Str(u["enabled"]) == "1"
		r.created, r.hasCreated = reportDT(u["created_at"])
		r.expires, r.hasExpires = reportDT(u["expiration"])
		rows = append(rows, r)
	}
	d.mu.Lock()
	if d.generation == generation {
		d.reportCache, d.reportCacheGenerate = rows, generation
	}
	d.mu.Unlock()
	return rows
}

func dayOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
}

func periodStart(day time.Time, period string) time.Time {
	switch period {
	case "week":
		return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7)) // Monday
	case "month":
		return time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.Local)
	}
	return day
}

func nextStart(start time.Time, period string) time.Time {
	switch period {
	case "week":
		return start.AddDate(0, 0, 7)
	case "month":
		return time.Date(start.Year(), start.Month()+1, 1, 0, 0, 0, 0, time.Local)
	}
	return start.AddDate(0, 0, 1)
}

func periodLabel(start time.Time, period string) string {
	switch period {
	case "week":
		year, week := start.ISOWeek()
		return fmt.Sprintf("%d-W%02d", year, week)
	case "month":
		return start.Format("2006-01")
	}
	return start.Format("2006-01-02")
}

func isoDay(t time.Time) string { return t.Format("2006-01-02") }

// countUpTo is bisect_right: how many of the sorted times are <= moment.
func countUpTo(sorted []time.Time, moment time.Time) int {
	return sort.Search(len(sorted), func(i int) bool { return sorted[i].After(moment) })
}

func BuildReport(d *Directory, period string, dateFrom, dateTo *string,
	profiles []int, managers []string, now time.Time) (M, error) {
	known := false
	for _, p := range periods {
		known = known || p == period
	}
	if !known {
		return nil, &ReportError{"period must be one of " + strings.Join(periods, ", ")}
	}
	today := dayOf(now)
	first, last := today.AddDate(0, 0, -29), today
	parse := func(s *string, fallback time.Time) (time.Time, error) {
		if s == nil || *s == "" {
			return fallback, nil
		}
		return time.ParseInLocation("2006-01-02", *s, time.Local)
	}
	var err1, err2 error
	first, err1 = parse(dateFrom, first)
	last, err2 = parse(dateTo, last)
	if err1 != nil || err2 != nil {
		return nil, &ReportError{"dates must be YYYY-MM-DD"}
	}
	if last.Before(first) {
		return nil, &ReportError{"'to' is before 'from'"}
	}

	type bound struct{ start, next time.Time }
	var bounds []bound
	for start := periodStart(first, period); !start.After(last); {
		next := nextStart(start, period)
		bounds = append(bounds, bound{start, next})
		if len(bounds) > maxPeriods {
			return nil, &ReportError{fmt.Sprintf("more than %d %ss -- pick a "+
				"shorter range or a longer period", maxPeriods, period)}
		}
		start = next
	}

	wantedProfiles := map[int]bool{}
	for _, p := range profiles {
		wantedProfiles[p] = true
	}
	wantedManagers := map[string]bool{}
	for _, m := range managers {
		wantedManagers[val.Fold(m)] = true
	}
	var users []reportUser
	for _, u := range d.parsedUsers() {
		if len(wantedProfiles) > 0 && !(u.hasProfile && wantedProfiles[u.profile]) {
			continue
		}
		if len(wantedManagers) > 0 && !wantedManagers[u.manager] {
			continue
		}
		users = append(users, u)
	}

	rangeStart, rangeEnd := bounds[0].start, bounds[len(bounds)-1].next
	index := map[string]int{} // period start date -> row number
	rows := make([]M, len(bounds))
	counts := make([][3]int, len(bounds)) // new, expired, expiring
	for i, b := range bounds {
		index[isoDay(b.start)] = i
		rows[i] = M{"period": periodLabel(b.start, period),
			"start": isoDay(b.start), "end": isoDay(b.next.AddDate(0, 0, -1)),
			"active": nil}
	}
	slot := func(moment time.Time) int {
		if moment.Before(rangeStart) || !moment.Before(rangeEnd) {
			return -1
		}
		if n, ok := index[isoDay(periodStart(dayOf(moment), period))]; ok {
			return n
		}
		return -1
	}

	// Active at a moment E = (# active spans started by E) - (# ended by E),
	// with a span [created, expiration) per enabled user. Two sorted lists
	// and a binary search per period end, instead of a pass over every user
	// each time.
	var starts, ends []time.Time
	noCreated, noExpiration := 0, 0
	for _, u := range users {
		if u.hasCreated {
			if n := slot(u.created); n >= 0 {
				counts[n][0]++
			}
		}
		if u.hasExpires {
			if n := slot(u.expires); n >= 0 {
				if !u.expires.After(now) {
					counts[n][1]++
				} else {
					counts[n][2]++
				}
			}
		}
		if !u.hasCreated {
			noCreated++
		}
		if !u.hasExpires {
			noExpiration++
			continue
		}
		if u.enabled {
			begin := time.Time{} // datetime.min
			if u.hasCreated {
				begin = u.created
			}
			if begin.Before(u.expires) {
				starts = append(starts, begin)
				ends = append(ends, u.expires)
			}
		}
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	sort.Slice(ends, func(i, j int) bool { return ends[i].Before(ends[j]) })
	activeAt := func(moment time.Time) int {
		return countUpTo(starts, moment) - countUpTo(ends, moment)
	}

	totals := [3]int{}
	for i, b := range bounds {
		rows[i]["new"], rows[i]["expired"], rows[i]["expiring"] =
			counts[i][0], counts[i][1], counts[i][2]
		for k := range totals {
			totals[k] += counts[i][k]
		}
		if b.start.After(now) {
			continue // a future period: no active figure
		}
		end := b.next.Add(-time.Second)
		if now.Before(end) {
			end = now
		}
		rows[i]["active"] = activeAt(end)
	}

	return M{
		"period": period, "from": isoDay(bounds[0].start),
		"to": rows[len(rows)-1]["end"], "rows": rows,
		"totals": M{
			"users":         len(users),
			"active_now":    activeAt(now),
			"new":           totals[0],
			"expired":       totals[1],
			"expiring":      totals[2],
			"no_created":    noCreated,
			"no_expiration": noExpiration,
		},
		"generated_at": now.Format("2006-01-02 15:04:05"),
	}, nil
}

// --------------------------------------------------------------------------
// FILES
// --------------------------------------------------------------------------

func ReportCSV(report M) []byte {
	var out bytes.Buffer
	out.WriteString("\ufeff")
	w := csv.NewWriter(&out)
	w.UseCRLF = true
	head := []string{}
	for _, c := range ReportColumns {
		head = append(head, c.Label)
	}
	w.Write(head)
	for _, row := range report["rows"].([]M) {
		line := []string{}
		for _, c := range ReportColumns {
			line = append(line, val.Str(row[c.Key]))
		}
		w.Write(line)
	}
	w.Flush()
	return out.Bytes()
}

func ReportXLSX(report M) ([]byte, error) {
	head := []any{}
	for _, c := range ReportColumns {
		head = append(head, c.Label)
	}
	lines := [][]any{head}
	for _, row := range report["rows"].([]M) {
		line := []any{}
		for _, c := range ReportColumns {
			line = append(line, row[c.Key])
		}
		lines = append(lines, line)
	}
	totals := report["totals"].(M)
	lines = append(lines, []any{},
		[]any{"Active now", totals["active_now"]},
		[]any{"Users in report", totals["users"]},
		[]any{"Without a creation date", totals["no_created"]},
		[]any{"Generated", report["generated_at"]})
	return writeSheet("Report", lines)
}
