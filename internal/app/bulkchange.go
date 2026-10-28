package app

// SAS4's own server-side Bulk Changes: one request, applied by SAS4 to every
// user matching a filter.
//
//	preview  POST index/user               {"bulk_filter": [...], page, count}
//	apply    POST bulkChanges/users/apply  {"filter": [...], "action": {...}}
//
// Not in the developer collection. The shapes come from a request captured on
// a live SAS4 server, which decrypts to
//
//	{"filter": [{"key": "profile_id", "op": "=", "value": 14,
//	             "value_label": "flat-Old", "id": "<uuid>"}],
//	 "action": {"name": "set_traffic_balance", "option": 92160}}
//
// and from the GUI's list of filter keys and actions. A filter's `value` is
// the id (the name goes in `value_label`), and an action's value goes in
// `option`. Actions without a value send only their name.
//
// Because one mistake reaches every matching account at once, BulkApply
// refuses unless all of this holds:
//
//   - there is at least one filter row -- an empty filter means every user;
//   - the preview just before sending still finds exactly the count the
//     person confirmed, so nothing changed under them between preview and
//     apply;
//   - the preview's sample rows actually satisfy the filter wherever that can
//     be checked here, which catches a server that silently ignored
//     bulk_filter and would have changed everyone.

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"sastoolkit/internal/sas4"
	"sastoolkit/internal/val"
)

const (
	bulkApplyRoute   = "bulkChanges/users/apply"
	bulkPreviewRoute = "index/user"
	bulkSampleSize   = 20
)

var bulkOps = []string{"=", "!=", "<", ">", "like"}

type filterKey struct{ Key, Label, Type string }

// Types: profile | manager | site | bool | date | number | text. The first
// three are picked by id and sent as integers.
var filterKeys = []filterKey{
	{"username", "Username", "text"},
	{"expiration", "Expiration", "date"},
	{"enabled", "Enabled", "bool"},
	{"firstname", "First Name", "text"},
	{"lastname", "Last Name", "text"},
	{"profile_id", "Profile", "profile"},
	{"parent_id", "Parent", "manager"},
	{"parent_and_subs", "Parent & Subs", "manager"},
	{"email", "Email", "text"},
	{"company", "Company", "text"},
	{"phone", "Phone", "text"},
	{"city", "City", "text"},
	{"address", "Address", "text"},
	{"contract_id", "Contract Number", "text"},
	{"national_id", "National ID", "text"},
	{"simultaneous_sessions", "Simultaneous Sessions", "number"},
	{"user_type", "User Type", "number"},
	{"static_ip", "Static IP", "text"},
	{"auto_renew", "Auto Renew", "bool"},
	{"site", "Site", "site"},
	{"group", "Group", "number"},
	{"creation_date", "Creation Date", "date"},
}

type bulkAction struct {
	Name, Label, Type string // Type "" = no value
	Danger            bool
}

// Option types: number | text | profile | site | manager | group | "" (no
// value). "export" is left out: it answers with a spreadsheet, not a change.
var bulkActions = []bulkAction{
	{"add_traffic", "Add Traffic (MB)", "number", false},
	{"add_traffic_dl", "Add Download Traffic (MB)", "number", false},
	{"add_traffic_ul", "Add Upload Traffic (MB)", "number", false},
	{"set_traffic_balance", "Set Traffic Balance (MB)", "number", true},
	{"add_days", "Increase Expiration (Days)", "number", false},
	{"add_hours", "Increase Expiration (Hours)", "number", false},
	{"set_expiration", "Set Expiration", "text", true},
	{"set_profile", "Set Profile", "profile", true},
	{"set_site", "Set Site", "site", false},
	{"set_parent", "Set Parent", "manager", true},
	{"set_group", "Set Group", "group", false},
	{"set_city", "Set City", "text", false},
	{"set_simultaneous_sessions", "Set Simultaneous Sessions", "number", false},
	{"enable", "Enable", "", false},
	{"disable", "Disable", "", true},
	{"set_auto_renew", "Enable Auto-Renew", "", false},
	{"unset_auto_renew", "Disable Auto-Renew", "", false},
	{"disconnect", "Disconnect", "", false},
	{"send_sms", "Send SMS", "text", false},
	{"delete", "Delete", "", true},
}

func isIDType(t string) bool {
	return t == "profile" || t == "manager" || t == "site" || t == "group"
}

// checkable: filters a sample row can be checked against here: key -> field.
var checkable = map[string]string{"profile_id": "profile_id",
	"parent_id": "parent_id", "enabled": "enabled", "username": "username",
	"simultaneous_sessions": "simultaneous_sessions"}

type BulkError struct{ Msg string }

func (e *BulkError) Error() string { return e.Msg }

func bulkErrf(format string, args ...any) error {
	return &BulkError{fmt.Sprintf(format, args...)}
}

func isBulkError(err error) bool {
	var b *BulkError
	return errors.As(err, &b)
}

func findKey(key string) (filterKey, bool) {
	for _, k := range filterKeys {
		if k.Key == key {
			return k, true
		}
	}
	return filterKey{}, false
}

func findAction(name string) (bulkAction, bool) {
	for _, a := range bulkActions {
		if a.Name == name {
			return a, true
		}
	}
	return bulkAction{}, false
}

func BulkDescribe() M {
	keys := []M{}
	for _, k := range filterKeys {
		keys = append(keys, M{"key": k.Key, "label": k.Label, "type": k.Type})
	}
	actions := []M{}
	for _, a := range bulkActions {
		var t any
		if a.Type != "" {
			t = a.Type
		}
		actions = append(actions, M{"name": a.Name, "label": a.Label,
			"type": t, "danger": a.Danger})
	}
	return M{"ops": bulkOps, "keys": keys, "actions": actions}
}

// --------------------------------------------------------------------------
// BUILDING THE BODY
// --------------------------------------------------------------------------

func labelOf(ctx Ctx, kind string, id int) string {
	if name, ok := ctx[kind+"s"].ByID[id]; ok {
		return name
	}
	return fmt.Sprint(id)
}

func asInt(raw any, what string) (int, error) {
	text := strings.TrimSpace(val.Str(raw))
	num, ok := val.Float(text)
	if !ok {
		return 0, bulkErrf("%s must be a number, not %s", what, pyRepr(raw))
	}
	if num != math.Trunc(num) {
		return 0, bulkErrf("%s must be a whole number, not %s", what, pyRepr(raw))
	}
	return int(num), nil
}

type BulkFilterRow struct {
	Key   string `json:"key"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

// BuildFilter turns UI rows [{key, op, value}] into the GUI's filter list.
func BuildFilter(rows []BulkFilterRow, ctx Ctx) ([]M, error) {
	out := []M{}
	for i, row := range rows {
		number := i + 1
		key, found := findKey(row.Key)
		if !found {
			return nil, bulkErrf("filter %d: unknown field '%s'", number, row.Key)
		}
		known := false
		for _, op := range bulkOps {
			known = known || op == row.Op
		}
		if !known {
			return nil, bulkErrf("filter %d: unknown operator '%s'", number, row.Op)
		}
		if val.BlankTrim(row.Value) {
			return nil, bulkErrf("filter %d (%s): no value", number, key.Label)
		}
		var value any
		var shown string
		if isIDType(key.Type) || key.Type == "number" || key.Type == "bool" {
			n, err := asInt(row.Value, key.Label)
			if err != nil {
				return nil, err
			}
			value = n
			switch key.Type {
			case "profile", "manager", "site":
				shown = labelOf(ctx, key.Type, n)
			case "bool":
				shown = "No"
				if n != 0 {
					shown = "Yes"
				}
			default:
				shown = fmt.Sprint(n)
			}
		} else {
			shown = strings.TrimSpace(val.Str(row.Value))
			value = shown
		}
		out = append(out, M{"key": row.Key, "op": row.Op, "value": value,
			"value_label": shown, "id": uuid4()})
	}
	if len(out) == 0 {
		return nil, bulkErrf("add at least one filter -- an empty filter " +
			"would change every user")
	}
	return out, nil
}

func BuildAction(name string, option any) (M, error) {
	a, ok := findAction(name)
	if !ok {
		return nil, bulkErrf("unknown action '%s'", name)
	}
	action := M{"name": name}
	if a.Type == "" {
		return action, nil
	}
	if val.BlankTrim(option) {
		return nil, bulkErrf("%s needs a value", a.Label)
	}
	if a.Type == "number" || isIDType(a.Type) {
		n, err := asInt(option, a.Label)
		if err != nil {
			return nil, err
		}
		if a.Type == "number" && n <= 0 && name != "set_traffic_balance" {
			return nil, bulkErrf("%s must be more than 0", a.Label)
		}
		if n < 0 {
			return nil, bulkErrf("%s cannot be negative", a.Label)
		}
		action["option"] = n
	} else {
		action["option"] = strings.TrimSpace(val.Str(option))
	}
	return action, nil
}

// --------------------------------------------------------------------------
// PREVIEW / APPLY
// --------------------------------------------------------------------------

// matches: 1 yes, 0 no, -1 cannot judge this one here.
func matches(record, row M) int {
	field, ok := checkable[val.Str(row["key"])]
	if !ok {
		return -1
	}
	have, present := record[field]
	if !present {
		return -1
	}
	want, op := row["value"], val.Str(row["op"])
	b := func(x bool) int {
		if x {
			return 1
		}
		return 0
	}
	switch op {
	case "=", "!=":
		same := val.Fold(strings.TrimSpace(val.Str(have))) ==
			val.Fold(strings.TrimSpace(val.Str(want)))
		return b(same == (op == "="))
	case "like":
		return b(strings.Contains(val.Fold(val.Str(have)),
			val.Fold(strings.Trim(val.Str(want), "%"))))
	}
	return -1
}

// BulkPreview -> {total, sample, problems}. `problems` lists sample rows that
// break the filter: a sign SAS4 ignored bulk_filter.
func BulkPreview(c *sas4.Client, filters []M) (M, error) {
	status, data := c.ReadPost(bulkPreviewRoute, M{
		"bulk_filter": filters, "page": 1, "count": bulkSampleSize,
		"sortBy": "id", "direction": "asc", "search": "",
		"columns": UserColumns})
	obj, _ := data.(M)
	rows, ok := obj["data"].([]any)
	total, totalOK := val.Int(obj["total"])
	if !ok || obj["total"] == nil || !totalOK {
		return nil, bulkErrf("preview failed (HTTP %d): %s", status,
			sas4.Message(data))
	}
	problems := []string{}
	sample := []M{}
	for _, r := range rows {
		record, _ := r.(M)
		for _, row := range filters {
			if matches(record, row) == 0 {
				problems = append(problems, fmt.Sprintf(
					"%s: %s=%s does not satisfy %s %s",
					val.Str(record["username"]), val.Str(row["key"]),
					val.Str(record[checkable[val.Str(row["key"])]]),
					val.Str(row["op"]), val.Str(row["value_label"])))
				break
			}
		}
		details, _ := record["profile_details"].(M)
		sample = append(sample, M{"user_id": record["id"],
			"username": record["username"], "firstname": record["firstname"],
			"lastname": record["lastname"], "profile_name": details["name"],
			"parent_username": record["parent_username"],
			"enabled":         record["enabled"], "expiration": record["expiration"]})
	}
	return M{"total": total, "sample": sample, "problems": problems}, nil
}

// BulkApply re-previews, checks, then sends -> (status, data, body).
func BulkApply(c *sas4.Client, filters []M, action M, expected int) (int, any, M, error) {
	check, err := BulkPreview(c, filters)
	if err != nil {
		return 0, nil, nil, err
	}
	problems := check["problems"].([]string)
	if len(problems) > 0 {
		return 0, nil, nil, bulkErrf("SAS4 returned users that do not match "+
			"the filter, so it may be ignoring it -- refusing to apply. %s",
			strings.Join(problems[:min(3, len(problems))], "; "))
	}
	total := check["total"].(int)
	if total != expected {
		return 0, nil, nil, bulkErrf("the filter now matches %d users, not "+
			"the %d you confirmed -- preview again", total, expected)
	}
	if total == 0 {
		return 0, nil, nil, bulkErrf("the filter matches nobody")
	}
	body := M{"filter": filters, "action": action}
	// Sent once, never retried: a timeout may come after SAS4 applied it.
	status, data := c.Call("POST", bulkApplyRoute, body, 0)
	return status, data, body, nil
}

func BulkSummarise(filters []M, action M) string {
	parts := []string{}
	for _, f := range filters {
		key, _ := findKey(val.Str(f["key"]))
		parts = append(parts, fmt.Sprintf("%s %s %s", key.Label,
			val.Str(f["op"]), val.Str(f["value_label"])))
	}
	a, _ := findAction(val.Str(action["name"]))
	value := ""
	if opt, ok := action["option"]; ok {
		value = " = " + val.Str(opt)
	}
	return a.Label + value + " where " + strings.Join(parts, " and ")
}

// --------------------------------------------------------------------------
// ONE USER AT A TIME (duplicate filters)
// --------------------------------------------------------------------------

// The app's own filters (duplicates, last session: see App.filterUsers) are
// worked out here, not by SAS4, so a bulk change that uses them cannot be
// one SAS4 request. Instead the users
// are matched here and the same bulkChanges/users/apply is sent once per
// user, filtered on that username alone.

const bulkEachShown = 1000 // preview rows sent to the browser

// BulkLocalMatch -> the rows (already narrowed by the app filters) that
// also pass the SAS4 condition rows, if any. With condition rows, SAS4 is
// asked for every user they match, and each one is checked against the rows
// wherever that can be done here, as BulkPreview does.
func BulkLocalMatch(c *sas4.Client, rows []M, filters []M) ([]M, error) {
	if len(filters) == 0 {
		return rows, nil
	}
	keep := map[string]bool{}
	problems := []string{}
	opt := sas4.DefaultPageOpts()
	opt.Extra = M{"bulk_filter": filters}
	err := c.Paged(bulkPreviewRoute, UserColumns, opt, func(record M) bool {
		for _, row := range filters {
			if matches(record, row) == 0 {
				problems = append(problems, fmt.Sprintf(
					"%s: %s=%s does not satisfy %s %s",
					val.Str(record["username"]), val.Str(row["key"]),
					val.Str(record[checkable[val.Str(row["key"])]]),
					val.Str(row["op"]), val.Str(row["value_label"])))
				return false
			}
		}
		keep[val.Fold(strings.TrimSpace(val.Str(record["username"])))] = true
		return true
	})
	if err != nil {
		return nil, bulkErrf("cannot read the users matching the "+
			"conditions: %s", err.Error())
	}
	if len(problems) > 0 {
		return nil, bulkErrf("SAS4 returned users that do not match the "+
			"filter, so it may be ignoring it. %s", problems[0])
	}
	out := []M{}
	for _, user := range rows {
		if keep[val.Fold(strings.TrimSpace(val.Str(user["username"])))] {
			out = append(out, user)
		}
	}
	return out, nil
}

// BulkLocalPreview -> {total, rows, mode}: the matched users with their last
// session (when scanned), grouped by the shared value if any.
func BulkLocalPreview(matched []M, last map[string]M) M {
	rows := []M{}
	for _, user := range matched[:min(len(matched), bulkEachShown)] {
		row := M{"user_id": user["user_id"], "username": user["username"],
			"firstname": user["firstname"], "lastname": user["lastname"],
			"profile_name":    user["profile_name"],
			"parent_username": user["parent_username"],
			"static_ip":       user["static_ip"]}
		for _, k := range DuplicateColumns {
			row[k] = user[k]
		}
		if session := last[val.Fold(strings.TrimSpace(val.Str(user["username"])))]; session != nil {
			for k, v := range sessionFields(session) {
				if row[k] == nil {
					row[k] = v
				}
			}
		}
		rows = append(rows, row)
	}
	return M{"total": len(matched), "rows": rows, "mode": "each"}
}

func bulkEachBody(row, params M) (M, error) {
	action, _ := params["action"].(M)
	if action == nil {
		return nil, bulkErrf("no action")
	}
	name := strings.TrimSpace(val.Str(row["username"]))
	if name == "" {
		return nil, skipf("no username")
	}
	filters, err := BuildFilter([]BulkFilterRow{{Key: "username", Op: "=",
		Value: name}}, nil)
	if err != nil {
		return nil, err
	}
	return M{"filter": filters, "action": action}, nil
}

func buildBulkEach(row, params M, ctx Ctx) (M, error) {
	return bulkEachBody(row, params)
}

// runBulkEach applies the action to one user. The username filter is
// previewed first and must find exactly this user, in case SAS4 treats "="
// as a substring match.
func runBulkEach(c *sas4.Client, row, params M, ctx Ctx) (string, int, string, M) {
	body, err := bulkEachBody(row, params)
	if err != nil {
		return "skipped", 0, err.Error(), nil
	}
	filters := body["filter"].([]M)
	check, err := BulkPreview(c, filters)
	if err != nil {
		return "failed", 0, err.Error(), body
	}
	sample, _ := check["sample"].([]M)
	name := val.Str(row["username"])
	if check["total"].(int) != 1 || len(sample) != 1 ||
		val.Fold(strings.TrimSpace(val.Str(sample[0]["username"]))) !=
			val.Fold(strings.TrimSpace(name)) {
		return "skipped", 0, fmt.Sprintf("the username filter matches %d "+
			"users, not just %s -- not sent", check["total"], name), body
	}
	// Sent once, never retried: a timeout may come after SAS4 applied it.
	status, data := c.Call("POST", bulkApplyRoute, body, 0)
	action := "failed"
	if sas4.Succeeded(status, data) {
		action = "done"
	}
	return action, status, sas4.Message(data), body
}
