package app

// Background runs of one operation over many rows.
//
// Each finished row is appended to reports/bulk_<op>_<HH-MM-SS>.csv straight
// away, so a crash or a closed browser loses nothing already sent. "Retry
// failed" starts a new job from the failed rows only; rows that went through
// are never sent again.

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sastoolkit/internal/sas4"
	"sastoolkit/internal/val"
)

// ReportsDir is where job reports are written.
var ReportsDir = "reports"

const MaxWorkers = 16

var rowColumns = []string{"line", "user_id", "username", "manager_id",
	"manager_username", "firstname", "lastname", "profile_name",
	"parent_username", "expiration"}
var resultColumns = []string{"action", "http_status", "message",
	"transaction_id", "payload", "timestamp"}

var jobIDs atomic.Int64

func nextJobID() int { return int(jobIDs.Add(1)) }

type UnverifiedRefused struct{ Msg string }

func (e *UnverifiedRefused) Error() string { return e.Msg }

// Runner is what the job board holds: a per-row Job or a BulkChangeJob.
type Runner interface {
	ID() int
	Owner() string
	Summary(recent int) M
	Cancel()
	Running() bool
	IsDryRun() bool
	FailedRows() []M
	ReportPath() string
	start()
}

func stamp() string { return time.Now().Format("15-04-05") }

func timestamp() string { return time.Now().Format("2006-01-02 15:04:05") }

func shownPath(path string) string {
	if wd, err := os.Getwd(); err == nil {
		if abs, err := filepath.Abs(path); err == nil {
			if rel, err := filepath.Rel(wd, abs); err == nil &&
				!strings.HasPrefix(rel, "..") {
				return rel
			}
			return abs
		}
	}
	return path
}

func optFloat(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return float64(t.UnixNano()) / 1e9
}

type result struct {
	row M
	res M
}

type Job struct {
	id       int
	client   *sas4.Client
	Op       *Operation
	rows     []M
	Params   M
	ctx      Ctx
	dryRun   bool
	Workers  int
	owner    string
	retryOf  any
	path     string
	columns  []string
	cancel   atomic.Bool
	mu       sync.Mutex
	counts   map[string]int
	results  []result // in finish order
	state    string   // queued | running | done | cancelled
	started  time.Time
	finished time.Time
}

func NewJob(client *sas4.Client, op *Operation, rows []M, params M, ctx Ctx,
	dryRun bool, workers int, owner string, allowUnverified bool,
	retryOf any) (*Job, error) {
	if !op.Verified && !dryRun && len(rows) > 1 && !allowUnverified {
		return nil, &UnverifiedRefused{fmt.Sprintf(
			"'%s' is not in the API docs. Run it on ONE row first and check "+
				"the result in SAS4, then restart the app with "+
				"--allow-unverified to run it in bulk.", op.Label)}
	}
	if workers == 0 {
		workers = 4
	}
	workers = max(1, min(workers, MaxWorkers))
	if params == nil {
		params = M{}
	}
	id := nextJobID()
	suffix := ""
	if dryRun {
		suffix = ".dryrun"
	}
	csvKeys := map[string]bool{}
	for _, r := range rows {
		for k := range csvOf(r) {
			csvKeys[k] = true
		}
	}
	inRow := map[string]bool{}
	for _, c := range rowColumns {
		inRow[c] = true
	}
	extra := []string{}
	for k := range csvKeys {
		if !inRow[k] {
			extra = append(extra, "csv_"+k)
		}
	}
	sort.Strings(extra)
	columns := append(append(append([]string{}, rowColumns...), extra...),
		resultColumns...)
	return &Job{id: id, client: client, Op: op, rows: rows, Params: params,
		ctx: ctx, dryRun: dryRun, Workers: workers, owner: owner,
		retryOf: retryOf, columns: columns, counts: map[string]int{},
		state: "queued",
		path: filepath.Join(ReportsDir, fmt.Sprintf("bulk_%s%s_%s_%d.csv",
			op.Key, suffix, stamp(), id))}, nil
}

func (j *Job) ID() int            { return j.id }
func (j *Job) Owner() string      { return j.owner }
func (j *Job) IsDryRun() bool     { return j.dryRun }
func (j *Job) ReportPath() string { return j.path }
func (j *Job) Cancel()            { j.cancel.Store(true) }

func (j *Job) Running() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state == "running"
}

func (j *Job) start() { go j.run() }

func (j *Job) run() {
	j.mu.Lock()
	j.state, j.started = "running", time.Now()
	j.mu.Unlock()
	defer func() {
		j.mu.Lock()
		j.finished = time.Now()
		j.state = "done"
		if j.cancel.Load() {
			j.state = "cancelled"
		}
		j.mu.Unlock()
	}()

	var handle *os.File
	var writer *csv.Writer
	if err := os.MkdirAll(filepath.Dir(j.path), 0o755); err == nil {
		handle, _ = os.Create(j.path)
	}
	if handle != nil {
		defer handle.Close()
		handle.WriteString("\ufeff")
		writer = csv.NewWriter(handle)
		writer.UseCRLF = true
		writer.Write(j.columns)
		writer.Flush()
	}

	one := func(row M) {
		var res M
		if j.cancel.Load() {
			res = M{"action": "cancelled"}
		} else {
			res = j.execute(row)
		}
		res["timestamp"] = timestamp()
		line := M{}
		for _, k := range rowColumns {
			line[k] = row[k]
		}
		for k, v := range csvOf(row) {
			line["csv_"+k] = v
		}
		for k, v := range res {
			line[k] = v
		}
		out := make([]string, len(j.columns))
		for i, k := range j.columns {
			out[i] = val.Str(line[k])
		}
		j.mu.Lock()
		defer j.mu.Unlock()
		if writer != nil {
			writer.Write(out)
			writer.Flush()
		}
		j.results = append(j.results, result{row, res})
		j.counts[val.Str(res["action"])]++
	}

	queue := make(chan M)
	var wg sync.WaitGroup
	for range j.Workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for row := range queue {
				one(row)
			}
		}()
	}
	for _, row := range j.rows {
		queue <- row
	}
	close(queue)
	wg.Wait()
}

// execute runs one row; a bug must not kill the run.
func (j *Job) execute(row M) (res M) {
	defer func() {
		if r := recover(); r != nil {
			res = M{"action": "failed", "message": fmt.Sprintf("panic: %v", r)}
		}
	}()
	return Execute(j.client, j.Op, row, j.Params, j.ctx, j.dryRun)
}

func (j *Job) FailedRows() []M {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := []M{}
	for _, r := range j.results {
		if r.res["action"] == "failed" {
			out = append(out, r.row)
		}
	}
	return out
}

func (j *Job) Summary(recent int) M {
	j.mu.Lock()
	defer j.mu.Unlock()
	tail := []M{}
	if recent > 0 {
		from := max(0, len(j.results)-recent)
		for _, r := range j.results[from:] {
			item := M{"username": r.row["username"],
				"manager_username": r.row["manager_username"],
				"line":             r.row["line"]}
			for k, v := range r.res {
				item[k] = v
			}
			tail = append(tail, item)
		}
	}
	counts := M{}
	for k, v := range j.counts {
		counts[k] = v
	}
	params := M{}
	for k, v := range j.Params {
		if (k == "password" || k == "pin") && val.Truthy(v) {
			v = "***"
		}
		params[k] = v
	}
	return M{
		"id": j.id, "op": j.Op.Key, "label": j.Op.Label,
		"dry_run": j.dryRun, "state": j.state,
		"total": len(j.rows), "done": len(j.results),
		"counts": counts, "workers": j.Workers,
		"report":  shownPath(j.path),
		"started": optFloat(j.started), "finished": optFloat(j.finished),
		"retry_of": j.retryOf, "recent": tail, "params": params,
	}
}

// --------------------------------------------------------------------------

type JobBoard struct {
	mu   sync.Mutex
	jobs map[int]Runner
}

func NewJobBoard() *JobBoard { return &JobBoard{jobs: map[int]Runner{}} }

func (b *JobBoard) Add(job Runner) Runner {
	b.mu.Lock()
	b.jobs[job.ID()] = job
	b.mu.Unlock()
	job.start()
	return job
}

func (b *JobBoard) Get(id int, owner string) Runner {
	b.mu.Lock()
	defer b.mu.Unlock()
	job := b.jobs[id]
	if job == nil || job.Owner() != owner {
		return nil
	}
	return job
}

func (b *JobBoard) Mine(owner string) []Runner {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []Runner{}
	for _, j := range b.jobs {
		if j.Owner() == owner {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(a, c int) bool { return out[a].ID() > out[c].ID() })
	return out
}

// --------------------------------------------------------------------------

// BulkChangeJob is one server-side Bulk Change, shown in the jobs list like a
// Job.
//
// SAS4 does the work, so there is a single request and a single report line:
// the filter, the action, how many users the preview counted, and what SAS4
// answered. There are no per-user rows to retry.
type BulkChangeJob struct {
	id          int
	client      *sas4.Client
	op          *Operation
	owner       string
	filters     []M
	action      M
	expected    int
	description string
	path        string
	mu          sync.Mutex
	state       string
	started     time.Time
	finished    time.Time
	result      M
}

func NewBulkChangeJob(client *sas4.Client, op *Operation, filters []M,
	action M, expected int, owner, description string) *BulkChangeJob {
	id := nextJobID()
	return &BulkChangeJob{id: id, client: client, op: op, owner: owner,
		filters: filters, action: action, expected: expected,
		description: description, state: "queued", result: M{},
		path: filepath.Join(ReportsDir, fmt.Sprintf("bulk_change_%s_%d.csv",
			stamp(), id))}
}

func (j *BulkChangeJob) ID() int            { return j.id }
func (j *BulkChangeJob) Owner() string      { return j.owner }
func (j *BulkChangeJob) IsDryRun() bool     { return false }
func (j *BulkChangeJob) ReportPath() string { return j.path }
func (j *BulkChangeJob) Cancel()            {} // one request: nothing to stop part-way
func (j *BulkChangeJob) FailedRows() []M    { return []M{} }
func (j *BulkChangeJob) start()             { go j.run() }

func (j *BulkChangeJob) Running() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state == "running"
}

func (j *BulkChangeJob) run() {
	j.mu.Lock()
	j.state, j.started = "running", time.Now()
	j.mu.Unlock()
	var res M
	status, data, body, err := BulkApply(j.client, j.filters, j.action, j.expected)
	switch {
	case err == nil:
		action := "failed"
		if sas4.Succeeded(status, data) {
			action = "done"
		}
		res = M{"action": action, "http_status": status,
			"message": sas4.Message(data)}
	case isBulkError(err):
		res = M{"action": "refused", "message": err.Error()}
	default:
		res = M{"action": "failed", "message": err.Error()}
	}
	res["timestamp"] = timestamp()
	request := ""
	if body != nil {
		request = val.Dumps(body)
	}
	if err := os.MkdirAll(filepath.Dir(j.path), 0o755); err == nil {
		if handle, err := os.Create(j.path); err == nil {
			handle.WriteString("\ufeff")
			w := csv.NewWriter(handle)
			w.UseCRLF = true
			w.Write([]string{"description", "users", "action", "http_status",
				"message", "request", "timestamp"})
			w.Write([]string{j.description, fmt.Sprint(j.expected),
				val.Str(res["action"]), val.Str(res["http_status"]),
				val.Str(res["message"]), request, val.Str(res["timestamp"])})
			w.Flush()
			handle.Close()
		}
	}
	j.mu.Lock()
	j.result = res
	j.finished = time.Now()
	j.state = "done"
	j.mu.Unlock()
}

func (j *BulkChangeJob) Summary(recent int) M {
	j.mu.Lock()
	defer j.mu.Unlock()
	finished := j.state == "done"
	action := val.Str(j.result["action"])
	counts := M{}
	done := 0
	recentRows := []M{}
	if finished {
		done = j.expected
		if action == "done" {
			counts[action] = j.expected
		} else {
			counts[action] = 1
		}
		if recent > 0 {
			item := M{"username": j.description}
			for k, v := range j.result {
				item[k] = v
			}
			recentRows = append(recentRows, item)
		}
	}
	return M{
		"id": j.id, "op": j.op.Key, "label": j.op.Label,
		"dry_run": false, "state": j.state, "total": j.expected,
		"done": done, "counts": counts,
		"workers": 1, "report": shownPath(j.path),
		"started": optFloat(j.started), "finished": optFloat(j.finished),
		"retry_of": nil, "description": j.description,
		"recent": recentRows, "params": M{},
	}
}
