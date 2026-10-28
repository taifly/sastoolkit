package app

// Every bulk action the app can run, declared once.
//
// An operation says which route it calls, which form fields it needs, and how
// to turn one selected row plus the form values into the request body. The UI
// builds its forms from this list (GET /api/meta), and jobs.go runs it -- so
// adding an action is one entry here.
//
// A row is a selected user ({user_id, username, profile_id, ...}) or manager
// ({manager_id, manager_username, ...}); an imported CSV's columns ride along
// in row["_csv"]. A non-blank CSV column with a field's name overrides the
// form value for that row (e.g. an `amount` column gives each user a
// different amount).
//
// Verified=false marks a request whose body is not in the collection and was
// inferred. The job runner only lets those run for real on a single row
// unless the app is started with --allow-unverified, so the first use is a
// test on one account.

import (
	"crypto/rand"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"sastoolkit/internal/sas4"
	"sastoolkit/internal/val"
)

// --------------------------------------------------------------------------
// FIELDS
// --------------------------------------------------------------------------

// Field types: number | text | password | select | checkbox | profile |
// manager | site | extension (the last four are selects the UI fills from
// /api/meta).
type Field struct {
	Name     string     `json:"name"`
	Label    string     `json:"label"`
	Type     string     `json:"type"`
	Default  any        `json:"default"`
	Required bool       `json:"required"`
	Options  [][]string `json:"options"`
	Help     string     `json:"help"`
}

type fieldOpt func(*Field)

func def(v any) fieldOpt             { return func(f *Field) { f.Default = v } }
func required() fieldOpt             { return func(f *Field) { f.Required = true } }
func help(text string) fieldOpt      { return func(f *Field) { f.Help = text } }
func options(o ...[]string) fieldOpt { return func(f *Field) { f.Options = o } }

func F(name, label, typ string, opts ...fieldOpt) Field {
	f := Field{Name: name, Label: label, Type: typ, Options: [][]string{}}
	for _, o := range opts {
		o(&f)
	}
	return f
}

var payMethod = F("method", "Pay with", "select", def("credit"), options(
	[]string{"credit", "Manager credit"},
	[]string{"reward_points", "Reward points"}))

var trafficUnits = map[string]float64{"MB": 1, "GB": 1024, "TB": 1024 * 1024}

// Skip: the row cannot be sent as it stands -- reported, not run.
type Skip struct{ Msg string }

func (s *Skip) Error() string { return s.Msg }

func skipf(format string, args ...any) error {
	return &Skip{fmt.Sprintf(format, args...)}
}

// value is the row's own CSV value for name when it has one, else the
// form's. Only row["_csv"] is consulted: the SAS4 record's fields (the user's
// current profile_id, enabled...) must never stand in for a form value.
func value(row, params M, name string, req bool) (any, error) {
	var raw any
	if c, ok := csvOf(row)[name]; ok && strings.TrimSpace(c) != "" {
		raw = c
	} else {
		raw = params[name]
	}
	if s, ok := raw.(string); ok {
		raw = strings.TrimSpace(s)
	}
	if req && val.Blank(raw) {
		return nil, skipf("no %s", name)
	}
	return raw, nil
}

// opt is value() for an optional field.
func opt(row, params M, name string) any {
	v, _ := value(row, params, name, false)
	return v
}

// optStr is `value(...) or default`.
func optStr(row, params M, name, fallback string) any {
	v := opt(row, params, name)
	if !val.Truthy(v) {
		return fallback
	}
	return v
}

// number: an int when whole (or asked for), else a float.
func number(row, params M, name string, req, integer, positive bool) (any, error) {
	raw, err := value(row, params, name, req)
	if err != nil || val.Blank(raw) {
		return nil, err
	}
	num, ok := val.Float(raw)
	if !ok {
		return nil, skipf("%s is not a number: %s", name, pyRepr(raw))
	}
	if positive && num <= 0 {
		return nil, skipf("%s must be more than 0", name)
	}
	if integer {
		return int64(math.RoundToEven(num)), nil
	}
	if num == math.Trunc(num) && math.Abs(num) < 1e15 {
		return int64(num), nil
	}
	return num, nil
}

func pyRepr(v any) string {
	if s, ok := v.(string); ok {
		return "'" + s + "'"
	}
	return val.Str(v)
}

func flag(row, params M, name string) bool {
	switch strings.ToLower(strings.TrimSpace(val.Str(opt(row, params, name)))) {
	case "1", "true", "yes", "on", "y":
		return true
	}
	return false
}

func uuid4() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func txid() string { return uuid4() }

// Lookup is a name <-> id table the payload builders resolve against.
type Lookup struct {
	ByID   map[int]string
	ByName map[string]int
}

// Ctx holds the "profiles", "sites" and "managers" tables.
type Ctx map[string]Lookup

// lookup resolves a profile / manager / site given by id or by name.
func lookup(ctx Ctx, kind string, raw any, req bool) (any, error) {
	if val.Blank(raw) {
		if req {
			return nil, skipf("no %s", kind)
		}
		return nil, nil
	}
	table := ctx[kind+"s"]
	text := strings.TrimSpace(val.Str(raw))
	if isDigits(text) {
		if n, err := strconv.Atoi(text); err == nil {
			if _, ok := table.ByID[n]; ok {
				return n, nil
			}
		}
	}
	if found, ok := table.ByName[val.Fold(text)]; ok {
		return found, nil
	}
	return nil, skipf("unknown %s: %s", kind, text)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// --------------------------------------------------------------------------
// OPERATION
// --------------------------------------------------------------------------

type BuildFunc func(row, params M, ctx Ctx) (M, error)

// RunFunc is a custom runner: -> (action, http status, message, body sent).
type RunFunc func(c *sas4.Client, row, params M, ctx Ctx) (string, int, string, M)

type Operation struct {
	Key        string
	Group      string
	Label      string
	Target     string // users | managers | new_users | server_bulk
	Method     string
	Route      string // formatted with the row: "user/{user_id}"
	Fields     []Field
	Build      BuildFunc
	Danger     bool
	Verified   bool
	Preview    string // GET route shown in a dry run
	Help       string
	CSVColumns []string
	Run        RunFunc
}

func (op *Operation) Describe() M {
	var preview any
	if op.Preview != "" {
		preview = op.Preview
	}
	fields, cols := op.Fields, op.CSVColumns
	if fields == nil {
		fields = []Field{}
	}
	if cols == nil {
		cols = []string{}
	}
	return M{"key": op.Key, "group": op.Group, "label": op.Label,
		"target": op.Target, "method": op.Method, "route": op.Route,
		"fields": fields, "danger": op.Danger, "verified": op.Verified,
		"preview": preview, "help": op.Help, "csv_columns": cols}
}

var placeholder = regexp.MustCompile(`\{(\w+)\}`)

func routeFor(template string, row M) (string, error) {
	var missing string
	out := placeholder.ReplaceAllStringFunc(template, func(m string) string {
		name := m[1 : len(m)-1]
		v, ok := row[name]
		if !ok || v == nil {
			if missing == "" {
				missing = name
			}
			return m
		}
		return val.Str(v)
	})
	if missing != "" {
		return "", skipf("no %s", missing)
	}
	return out, nil
}

// --------------------------------------------------------------------------
// PAYLOAD BUILDERS
// --------------------------------------------------------------------------

func uid(row M) (int, error) {
	if val.Blank(row["user_id"]) {
		return 0, skipf("user not found in SAS4")
	}
	n, ok := val.Int(row["user_id"])
	if !ok {
		return 0, skipf("bad user_id %s", val.Str(row["user_id"]))
	}
	return n, nil
}

func mid(row M) (int, error) {
	if val.Blank(row["manager_id"]) {
		return 0, skipf("manager not found in SAS4")
	}
	n, ok := val.Int(row["manager_id"])
	if !ok {
		return 0, skipf("bad manager_id %s", val.Str(row["manager_id"]))
	}
	return n, nil
}

func buildAddTraffic(row, params M, ctx Ctx) (M, error) {
	unitName := val.Str(optStr(row, params, "unit", "MB"))
	unit, ok := trafficUnits[strings.ToUpper(unitName)]
	if !ok {
		return nil, skipf("unknown unit %s", pyRepr(opt(row, params, "unit")))
	}
	amount, err := number(row, params, "amount", true, false, true)
	if err != nil {
		return nil, err
	}
	id, err := uid(row)
	if err != nil {
		return nil, err
	}
	f, _ := val.Float(amount)
	return M{"user_id": id, "username": row["username"],
		"amount":         int64(math.RoundToEven(f * unit)),
		"comment":        optStr(row, params, "comment", ""),
		"transaction_id": txid(),
		"target":         optStr(row, params, "target", "rxtx_mbytes")}, nil
}

func buildUserMoney(row, params M, ctx Ctx) (M, error) {
	id, err := uid(row)
	if err != nil {
		return nil, err
	}
	amount, err := number(row, params, "amount", true, false, true)
	if err != nil {
		return nil, err
	}
	return M{"user_id": id, "user_username": row["username"],
		"amount": amount, "comment": optStr(row, params, "comment", ""),
		"transaction_id": txid()}, nil
}

func buildActivate(row, params M, ctx Ctx) (M, error) {
	method := optStr(row, params, "method", "credit")
	pin := optStr(row, params, "pin", "")
	if val.Str(method) == "card" && !val.Truthy(pin) {
		return nil, skipf("card activation needs a pin")
	}
	id, err := uid(row)
	if err != nil {
		return nil, err
	}
	return M{"method": method, "pin": pin, "user_id": strconv.Itoa(id),
		"money_collected": flag(row, params, "money_collected"),
		"comments":        optStr(row, params, "comments", ""),
		"issue_invoice":   flag(row, params, "issue_invoice"),
		"transaction_id":  txid()}, nil
}

func buildExtend(row, params M, ctx Ctx) (M, error) {
	ext, err := number(row, params, "extension_id", true, true, true)
	if err != nil {
		return nil, err
	}
	id, err := uid(row)
	if err != nil {
		return nil, err
	}
	return M{"user_id": strconv.Itoa(id), "profile_id": val.Str(ext),
		"method":         optStr(row, params, "method", "credit"),
		"transaction_id": txid()}, nil
}

func buildChangeProfile(row, params M, ctx Ctx) (M, error) {
	raw, err := value(row, params, "profile_id", true)
	if err != nil {
		return nil, err
	}
	pid, err := lookup(ctx, "profile", raw, true)
	if err != nil {
		return nil, err
	}
	id, err := uid(row)
	if err != nil {
		return nil, err
	}
	return M{"user_id": strconv.Itoa(id), "profile_id": val.Str(pid),
		"method":         optStr(row, params, "method", "credit"),
		"transaction_id": txid()}, nil
}

func buildCreate(row, params M, ctx Ctx) (M, error) {
	username, err := value(row, params, "username", true)
	if err != nil {
		return nil, err
	}
	password, err := value(row, params, "password", true)
	if err != nil {
		return nil, err
	}
	enabled := 0
	if flag(row, params, "enabled") {
		enabled = 1
	}
	raw, err := value(row, params, "profile_id", true)
	if err != nil {
		return nil, err
	}
	profile, err := lookup(ctx, "profile", raw, true)
	if err != nil {
		return nil, err
	}
	raw, err = value(row, params, "parent_id", true)
	if err != nil {
		return nil, err
	}
	parent, err := lookup(ctx, "manager", raw, true)
	if err != nil {
		return nil, err
	}
	site, err := lookup(ctx, "site", opt(row, params, "site_id"), false)
	if err != nil {
		return nil, err
	}
	payload := M{"username": username, "enabled": enabled,
		"password": password, "confirm_password": password,
		"profile_id": profile, "parent_id": parent, "site_id": site}
	for _, name := range []string{"firstname", "lastname", "phone", "email",
		"address", "city", "company", "notes", "contract_id", "national_id",
		"static_ip", "expiration"} {
		if text := opt(row, params, name); !val.Blank(text) {
			payload[name] = text
		}
	}
	return payload, nil
}

func buildRename(row, params M, ctx Ctx) (M, error) {
	if _, err := uid(row); err != nil {
		return nil, err
	}
	nu, err := value(row, params, "new_username", true)
	if err != nil {
		return nil, err
	}
	if val.Str(nu) == val.Str(row["username"]) {
		return nil, skipf("new username is the same")
	}
	return M{"new_username": nu}, nil
}

func buildManagerMoney(row, params M, ctx Ctx) (M, error) {
	id, err := mid(row)
	if err != nil {
		return nil, err
	}
	amount, err := number(row, params, "amount", true, false, true)
	if err != nil {
		return nil, err
	}
	return M{"manager_id": id, "manager_username": row["manager_username"],
		"amount": amount, "comment": optStr(row, params, "comment", ""),
		"transaction_id": txid()}, nil
}

func buildManagerDeposit(row, params M, ctx Ctx) (M, error) {
	payload, err := buildManagerMoney(row, params, ctx)
	if err != nil {
		return nil, err
	}
	payload["is_loan"] = flag(row, params, "is_loan")
	return payload, nil
}

func buildPoints(row, params M, ctx Ctx) (M, error) {
	id, err := mid(row)
	if err != nil {
		return nil, err
	}
	amount, err := number(row, params, "amount", true, true, true)
	if err != nil {
		return nil, err
	}
	return M{"manager_id": id, "amount": amount}, nil
}

// runSetEnabled is unverified: the collection has no user update. Mirrors the
// documented manager update -- read the whole record, change `enabled`, post
// it back to the same id.
func runSetEnabled(c *sas4.Client, row, params M, ctx Ctx) (string, int, string, M) {
	userID, err := uid(row)
	if err != nil {
		return "skipped", 0, err.Error(), nil
	}
	status, data := c.Get(fmt.Sprintf("user/%d", userID))
	obj, _ := data.(M)
	record, ok := obj["data"].(M)
	if !sas4.Succeeded(status, data) || !ok {
		return "failed", status, "read user: " + sas4.Message(data), nil
	}
	want := 0
	switch strings.ToLower(val.Str(opt(row, params, "enabled"))) {
	case "1", "true", "enable", "enabled", "yes":
		want = 1
	}
	if val.Str(record["enabled"]) == strconv.Itoa(want) {
		word := "disabled"
		if want == 1 {
			word = "enabled"
		}
		return "skipped", status, "already " + word, nil
	}
	body := M{}
	for k, v := range record {
		switch v.(type) {
		case M, []any:
			continue
		}
		body[k] = v
	}
	body["enabled"] = want
	status, data = c.Call("POST", fmt.Sprintf("user/%d", userID), body, 0)
	action := "failed"
	if sas4.Succeeded(status, data) {
		action = "done"
	}
	return action, status, sas4.Message(data), body
}

// --------------------------------------------------------------------------
// THE REGISTRY
// --------------------------------------------------------------------------

const (
	gTraffic  = "Traffic & balance"
	gService  = "Service"
	gAccount  = "Account admin"
	gManagers = "Managers"
	gServer   = "Server-side"
)

func amountFields() []Field {
	return []Field{F("amount", "Amount", "number", required()),
		F("comment", "Comment", "text")}
}

var Operations = []*Operation{
	{Key: "add_traffic", Group: gTraffic, Label: "Add traffic", Target: "users",
		Method: "POST", Route: "user/addTraffic", Verified: true,
		Fields: []Field{
			F("amount", "Amount", "number", required()),
			F("unit", "Unit", "select", def("GB"), options(
				[]string{"MB", "MB"}, []string{"GB", "GB"}, []string{"TB", "TB"})),
			F("target", "Quota", "select", def("rxtx_mbytes"), options(
				[]string{"rxtx_mbytes", "Total (download + upload)"},
				[]string{"rx_mbytes", "Download only"},
				[]string{"tx_mbytes", "Upload only"})),
			F("comment", "Comment", "text")},
		Build: buildAddTraffic, CSVColumns: []string{"amount", "comment"}},
	{Key: "deposit", Group: gTraffic, Label: "Deposit balance", Target: "users",
		Method: "POST", Route: "user/deposit", Verified: true,
		Fields: amountFields(), Build: buildUserMoney,
		CSVColumns: []string{"amount", "comment"}},
	{Key: "withdraw", Group: gTraffic, Label: "Withdraw balance", Target: "users",
		Method: "POST", Route: "user/withdraw", Verified: true,
		Fields: amountFields(), Build: buildUserMoney, Danger: true,
		CSVColumns: []string{"amount", "comment"}},

	{Key: "activate", Group: gService, Label: "Activate / renew", Target: "users",
		Method: "POST", Route: "user/activate", Verified: true,
		Fields: []Field{
			F("method", "Pay with", "select", def("credit"), options(
				[]string{"credit", "Manager credit"},
				[]string{"reward_points", "Reward points"},
				[]string{"card", "Card (PIN)"})),
			F("pin", "Card PIN", "text", help("only for Card")),
			F("money_collected", "Money collected", "checkbox", def(true)),
			F("issue_invoice", "Issue invoice", "checkbox", def(true)),
			F("comments", "Comment", "text")},
		Build: buildActivate, Preview: "user/activationData/{user_id}",
		Help:       "Renews each user on their current profile and charges it.",
		CSVColumns: []string{"pin", "comments"}},
	{Key: "extend", Group: gService, Label: "Extend service", Target: "users",
		Method: "POST", Route: "user/extend", Verified: true,
		Fields: []Field{
			F("extension_id", "Extension", "extension", required(),
				help("extensions allowed for the selected users' profile")),
			payMethod},
		Build: buildExtend, Preview: "user/extensionData/{user_id}",
		CSVColumns: []string{"extension_id"}},
	{Key: "change_profile", Group: gService, Label: "Change profile",
		Target: "users", Method: "POST", Route: "user/changeProfile",
		Verified: true,
		Fields: []Field{F("profile_id", "New profile", "profile", required()),
			payMethod},
		Build:      buildChangeProfile,
		Help:       "An active user moves on their next expiration date.",
		CSVColumns: []string{"profile_id"}},
	{Key: "cancel", Group: gService, Label: "Cancel service (refund)",
		Target: "users", Method: "GET", Route: "user/refund/{user_id}",
		Danger: true, Verified: true, Preview: "user/refundData/{user_id}"},

	{Key: "create", Group: gAccount, Label: "Create users", Target: "new_users",
		Method: "POST", Route: "user", Verified: true,
		Fields: []Field{
			F("password", "Password (when the CSV has none)", "text"),
			F("profile_id", "Profile", "profile", required()),
			F("parent_id", "Owner manager", "manager", required()),
			F("site_id", "Site", "site"),
			F("enabled", "Enabled", "checkbox", def(true))},
		Build: buildCreate,
		Help: "Import a CSV with a username column. Existing usernames are " +
			"skipped.",
		CSVColumns: []string{"username", "password", "firstname", "lastname",
			"phone", "email", "address", "city", "profile_id", "parent_id",
			"site_id", "notes", "contract_id"}},
	{Key: "rename", Group: gAccount, Label: "Rename users", Target: "users",
		Method: "POST", Route: "user/rename/{user_id}", Verified: true,
		Fields: []Field{}, Build: buildRename,
		Help:       "Import a CSV with username and new_username columns.",
		CSVColumns: []string{"username", "new_username"}},
	{Key: "delete", Group: gAccount, Label: "Delete users", Target: "users",
		Method: "DELETE", Route: "user/{user_id}", Danger: true, Verified: true},
	{Key: "set_enabled", Group: gAccount, Label: "Enable / disable",
		Target: "users", Method: "POST", Route: "user/{user_id}",
		Fields: []Field{F("enabled", "Set to", "select", def("0"), options(
			[]string{"1", "Enabled"}, []string{"0", "Disabled"}))},
		Run: runSetEnabled, Verified: false,
		Help: "Not in the API docs: reads the user and posts it back with " +
			"`enabled` changed."},

	{Key: "m_deposit", Group: gManagers, Label: "Manager deposit",
		Target: "managers", Method: "POST", Route: "manager/deposit",
		Verified: true,
		Fields: []Field{F("amount", "Amount", "number", required()),
			F("comment", "Comment", "text"),
			F("is_loan", "As a loan", "checkbox", def(false))},
		Build: buildManagerDeposit, CSVColumns: []string{"amount", "comment"}},
	{Key: "m_withdraw", Group: gManagers, Label: "Manager withdraw",
		Target: "managers", Method: "POST", Route: "manager/withdraw",
		Verified: true, Fields: amountFields(), Build: buildManagerMoney,
		Danger: true, CSVColumns: []string{"amount", "comment"}},
	{Key: "m_add_points", Group: gManagers, Label: "Add reward points",
		Target: "managers", Method: "POST", Route: "manager/addRewardPoints",
		Verified: true,
		Fields:   []Field{F("amount", "Points", "number", required())},
		Build:    buildPoints, CSVColumns: []string{"amount"}},
	{Key: "m_deduct_points", Group: gManagers, Label: "Deduct reward points",
		Target: "managers", Method: "POST", Route: "manager/deductRewardPoints",
		Verified: true,
		Fields:   []Field{F("amount", "Points", "number", required())},
		Build:    buildPoints, Danger: true, CSVColumns: []string{"amount"}},
	{Key: "m_pay_debt", Group: gManagers, Label: "Pay debt", Target: "managers",
		Method: "POST", Route: "manager/payDebt", Verified: false,
		Fields: amountFields(), Build: buildManagerMoney,
		Help: "The API docs give no body for this; the deposit shape is " +
			"assumed.",
		CSVColumns: []string{"amount", "comment"}},

	// Not run row by row: bulkchange.go builds one filter + action request
	// and SAS4 applies it. The UI gives it its own filter builder.
	{Key: "bulk_change", Group: gServer, Label: "Bulk change",
		Target: "server_bulk", Method: "POST", Route: "bulkChanges/users/apply",
		Danger: true, Verified: true,
		Help: "SAS4 applies one action to every user matching the filter, in a " +
			"single request. Preview first; the count you confirm is checked " +
			"again just before sending."},
	// Bulk change with a duplicate filter (duplicates.go): matched here and
	// sent once per user. Started from /api/bulk/apply only, so it is not
	// listed in the UI.
	{Key: "bulk_change_each", Group: gServer, Label: "Bulk change (per user)",
		Target: "server_bulk_each", Method: "POST", Route: bulkApplyRoute,
		Danger: true, Verified: true, Build: buildBulkEach, Run: runBulkEach},
}

var ByKey = func() map[string]*Operation {
	out := map[string]*Operation{}
	for _, op := range Operations {
		out[op.Key] = op
	}
	return out
}()

// --------------------------------------------------------------------------
// EXECUTION
// --------------------------------------------------------------------------

func summarise(payload M) string {
	if payload == nil {
		return ""
	}
	shown := val.Copy(payload)
	for _, secret := range []string{"password", "confirm_password", "pin"} {
		if val.Truthy(shown[secret]) {
			shown[secret] = "***"
		}
	}
	return val.Cut(val.Dumps(shown), 400)
}

// Execute runs one row -> {action, http_status, message, transaction_id,
// payload}. Never fails for a bad row; a Skip becomes action=skipped.
func Execute(c *sas4.Client, op *Operation, row, params M, ctx Ctx, dryRun bool) M {
	route, err := routeFor(op.Route, row)
	var payload M
	if err == nil && op.Build != nil {
		payload, err = op.Build(row, params, ctx)
	}
	if err != nil {
		return M{"action": "skipped", "message": err.Error()}
	}

	tx := ""
	if payload != nil {
		tx = val.Str(payload["transaction_id"])
	}
	if dryRun {
		msg := ""
		if op.Preview != "" {
			if previewRoute, err := routeFor(op.Preview, row); err != nil {
				msg = err.Error()
			} else {
				_, data := c.Get(previewRoute)
				shown := data
				if obj, ok := data.(M); ok {
					if inner, has := obj["data"]; has {
						shown = inner
					}
				}
				msg = val.Cut(val.Dumps(shown), 400)
			}
		}
		return M{"action": "would_run", "message": msg,
			"payload": summarise(payload)}
	}

	if op.Run != nil {
		action, status, msg, body := op.Run(c, row, params, ctx)
		return M{"action": action, "http_status": status, "message": msg,
			"payload": summarise(body)}
	}

	// Writes are sent once: a timeout after the server applied it would
	// double it on a retry. Call still re-logs-in on a 401.
	var body any
	if op.Method != "GET" && payload != nil {
		body = payload
	}
	status, data := c.Call(op.Method, route, body, 0)
	action := "failed"
	if sas4.Succeeded(status, data) {
		action = "done"
	}
	return M{"action": action, "http_status": status,
		"message": sas4.Message(data), "transaction_id": tx,
		"payload": summarise(payload)}
}
