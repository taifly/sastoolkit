# SAS toolkit API

This is the local HTTP API the app's UI (`internal/app/static/app.js`) talks
to. The routes are in `routes()` in `internal/app/server.go`. Most of them
end in calls to SAS4, which are described in [SAS4_API.md](SAS4_API.md).

## Basics

### Where it runs

| Mode | Address |
|---|---|
| `--web` | `http://127.0.0.1:8765/` (`--bind`, `--port`) |
| Desktop window | a random `127.0.0.1` port, opened by the window only |

Requests and responses are JSON, except file uploads (multipart) and
downloads (CSV or XLSX).

### Sessions

Signing in to the app means signing in to SAS4. `POST /api/login` checks the
credentials against SAS4. It then sets an `HttpOnly`, `SameSite=Strict`
cookie named `sastoolkit`. Each cookie has its own:

- SAS4 client and token;
- user and manager list (the "directory");
- last-session scan;
- CSV imports (the last 20);
- jobs.

Sessions live in memory and are lost when the app restarts.

### The desktop guard

In the desktop window, every request must also carry the
`sastoolkit_window` cookie. The window gets it by opening `/?k=<secret>`,
with a secret that is new on every launch. A request without the cookie gets:

    403 {"detail": "open this app from its window"}

### Errors

Errors are `{"detail": "<message>"}`, with these statuses:

| Status | Meaning |
|---|---|
| 400 | The request makes no sense: unknown format, no rows selected, a bad filter... |
| 401 | Not logged in, or SAS4 rejected the login |
| 403 | The login URL is locked (`--lock-url`, or `--bind` to the network) |
| 404 | Unknown job, operation or import |
| 409 | Not ready yet: the user list is still loading, or a job is still running |
| 422 | The body is missing or is not valid JSON, or a required field is missing |
| 502 | SAS4 failed: profiles at login, a bulk change preview, or session history |

### Downloads and the Save dialog

Routes that return a file send it as an attachment. In the desktop window,
the UI adds the header `X-Save-Dialog: 1`. The app then shows a native Save
dialog and answers JSON instead:

- `{"saved": "<path>"}` when the file was saved;
- `{"cancelled": true}` when the person cancelled.

### Common shapes

**`filters`**: selects users from the loaded list. Every page that picks
users takes the same object: Select users, export, jobs, Bulk change
(`extra`), and both reports. Every field is optional, and the fields are
combined with AND.

```json
{"profiles": [3, 14], "managers": ["admin"], "statuses": ["active", "online"],
 "search": "ali", "expires_from": "2026-10-01", "expires_to": "2026-10-31",
 "duplicates": ["session_mac"], "session_states": ["online"],
 "mac": "aabbcc", "nas_ip": "10.255.", "stop_reasons": ["Lost-Carrier"],
 "upload_min": 1048576, "upload_max": null, "download_min": null,
 "download_max": null, "total_min": null, "total_max": 10737418240}
```

`statuses` takes `active`, `expired`, `disabled` and `online`.

The last-session filters read each user's newest session from the
[session scan](#post-apisessionsrefresh). Without a scan the answer is 409
`scan the sessions first`.

- `session_states`: `online`, `offline`, or `none` (no session in the
  look-back window). A user with no session fails every other session
  filter.
- `mac`: contained in the session's MAC. Case and the separators `:` `-`
  `.` are ignored.
- `nas_ip`: contained in the session's NAS IP.
- `stop_reasons`: the session's stop reason is one of these (case is
  ignored). The choices are in [`stop_reasons`](#get-apisessionsstatus).
- `upload_*`, `download_*`, `total_*`: the session's traffic in bytes,
  inclusive. `null` means no bound.

With any of these set, the rows are copies with `session_mac`, `session_ip`,
`session_state`, `session_start`, `nas_ip`, `session_upload`,
`session_download`, `session_total` and `session_stop_reason` added.

`duplicates` keeps users who share a value with at least one other user.
It takes `session_mac` (the MAC of the last session), `session_ip` (the IP of
the current session, online users only) and `static_ip` (the user record).
Several kinds are combined with OR. Duplicates are found across the whole
user list, then the other filters apply. The two session kinds need a
[session scan](#post-apisessionsrefresh); without one the answer is 409
`scan the sessions first`. Matching rows are copies with `dup_value`,
`dup_count` and the session fields above added, sorted by `dup_value`.

**Paged answer**: `{"total", "page", "page_size", "rows"}`. `page_size` is
capped at 500.

## Session

### `POST /api/login`

```json
{"username": "manager", "password": "<password>", "url": "sas.example.com"}
```

`url` is optional, and defaults to `--host`. It is reduced to
`scheme://host[:port]`, and `https` is assumed when no scheme is given.

The app then:

1. logs in to SAS4 (`POST login`);
2. loads the profiles (`GET list/profile/0`) and the sites (`GET site`);
3. starts loading the users and managers in the background.

Answer: `{"ok": true, "username", "host"}`, and the `sastoolkit` cookie.

### `POST /api/logout`

Ends the session and clears the cookie. Answer: `{"ok": true}`.

### `GET /api/session`

Works without a session.

```json
{"logged_in": true, "host": "https://sas.example.com", "default_host": "...",
 "allow_url_change": true, "username": "manager", "allow_unverified": false,
 "desktop": true}
```

## Reference data

### `GET /api/meta`

Everything the UI needs to build its forms:

- `host`, `username` and `allow_unverified`.
- `profiles`, `sites` and `managers`, each a list of `{id, name}`.
- `operations`: the [operation registry](#operations), with fields, routes
  and flags.
- `bulk`: the bulk change filter keys, operators and actions.

### `GET /api/directory`

The loading state of the user and manager lists:

```json
{"state": "loading", "loaded": 2500, "total": 5873, "users": 2500,
 "managers": 0, "browsable": 2500, "error": null, "manager_error": null,
 "fields": ["username", "..."], "loaded_at": null}
```

- `state` is `empty`, `loading`, `ready` or `error`.
- `fields` lists every user field, for the export column picker.

The users are read from `POST index/user`, page by page. The managers are
read from `POST index/manager`, with `GET index/manager` as a fallback.

### `POST /api/directory/refresh`

Loads the lists from SAS4 again. Answer: the same shape as
`GET /api/directory`.

### `GET /api/extensions/{profile_id}`

The extensions allowed for a profile, for the Extend form. Answer:
`{"extensions": [{"id", "name"}]}`. SAS4: `GET allowedExtensions/{profile_id}`.

## Selection

### `POST /api/users/query`

```json
{"filters": { ... }, "page": 1, "page_size": 50}
```

Answer: a paged answer, plus:

- `partial: true` while the list is still loading;
- `profiles`: the top 20 profiles in the result, as `[name, count]`.

This route works while the list is still loading, over the users read so far.

### `POST /api/users/export`

```json
{"filters": { ... }, "ids": [12, 15], "columns": [{"key": "username", "label": "Username"}],
 "format": "csv"}
```

- `ids` limits the export to ticked rows. Leave it out to export every
  matching row.
- With no `columns`, every field is exported.
- `format` is `csv` or `xlsx`.

The answer is the file, with an `X-Row-Count` header. Returns 409 until the
list has loaded completely.

### `POST /api/managers/query`

```json
{"search": "", "page": 1, "page_size": 50}
```

Answer: a paged answer of managers. Each row has `manager_id`,
`manager_username`, `firstname`, `lastname`, `balance`, `loan_balance` and
`users_count`.

### `POST /api/reports`

```json
{"period": "day", "date_from": "2026-10-01", "date_to": "2026-10-25",
 "filters": { ... }, "format": null}
```

- `period` is `day`, `week` or `month`.
- [`filters`](#common-shapes) picks the users counted. The older top-level
  `profiles` and `managers` still work, and are added to it.
- The dates default to the last 30 days.

Answer: `{period, from, to, rows, totals, generated_at}`.

- Each row has the period's `new`, `expired` and `expiring` counts, and the
  number of `active` users at the period's end.
- `totals` has `users`, `active_now`, `new`, `expired`, `expiring`,
  `no_created` and `no_expiration`.

With `format` set to `csv` or `xlsx`, the answer is the file. No SAS4 call:
the report is built from the loaded list.

### `POST /api/import`

A multipart form with `op` (an operation key) and `file` (a CSV). The
delimiter can be `,`, `;` or tab.

The rows are matched to the loaded users or managers by `username` (or
`manager_username`). A one-column file with no header counts as a list of
usernames.

```json
{"import_id": 3, "name": "list.csv", "columns": ["amount", "username"],
 "matched": 120, "unmatched_count": 2, "unmatched": [{"username": "x", "line": 7}],
 "preview": [ ... ]}
```

`preview` holds the first 50 matched rows, with passwords masked. Use
`import_id` as a job source.

## Last sessions

### `POST /api/sessions/refresh`

```json
{"lookback_days": 90}
```

Starts a scan of `POST index/UserSessions`, newest session first.

- `lookback_days` is `30`, `90`, `180`, `365` or `0` (the whole history).
- The scan stops when every user has been seen, or at the look-back limit.

Answer: the same shape as `GET /api/sessions/status`.

### `GET /api/sessions/status`

```json
{"state": "loading", "error": null, "lookback_days": 90,
 "progress": {"pages": 3, "sessions": 1500, "seen": 900, "users": 5873, "...": "..."},
 "stopped": null, "scanned_at": null, "stop_reasons": ["Lost-Carrier", "User-Request"]}
```

`stop_reasons` lists the distinct stop reasons in the last complete scan.

`stopped` says why the scan ended:

- `every user found`;
- `reached sessions older than N days`;
- `end of the session history`.

### `POST /api/sessions/query`

```json
{"filters": {"session_states": ["online"], ...}, "page": 1,
 "page_size": 100, "format": null}
```

The older top-level `states` field still works, and is added to
`filters.session_states`.

There is one row for each user, with their last session. `none` means the
scan found no session for that user. Each row has:

- username, name, profile, manager and expiration;
- `state`, `start`, `stop` and the duration;
- IP, MAC, NAS IP and port, and called station;
- upload, download and total, in bytes and in readable units;
- the stop reason.

Answer: a paged answer, plus the scan status and `totals`
(`users`, `online`, `offline`, `none`). With `format` set to `csv` or `xlsx`,
the answer is the file. Returns 409 until a scan has finished.

### `POST /api/sessions/history`

```json
{"username": "ali", "cursor": 1}
```

Returns up to 50 of one user's sessions, newest first:
`{username, cursor, next, rows, user}`. To read more, send `next` as the
`cursor`. `next` is `null` at the end.

SAS4: `POST index/UserSessions` with `search` set to the username. Only exact
matches are kept, and at most 10 SAS4 pages are read per call.

## Bulk change

### `POST /api/bulk/preview`

```json
{"filters": [{"key": "profile_id", "op": "=", "value": 14}]}
```

The filter is built into SAS4's shape: ids, `value_label` and row ids. The
app then calls `POST index/user` with `bulk_filter`.

Answer: `{total, sample, problems, filter}`.

- `sample` holds the first 20 matching users.
- `problems` lists sample rows that do not match the filter. These mean
  SAS4 ignored the filter.

With `"extra": { ... }` (a [`filters`](#common-shapes) object) that has any
field set, the match is worked out by the app instead, and `filters` may
then be empty. If conditions are given, every user SAS4 matches is read
(`index/user` with `bulk_filter`, all pages), and only the users who also
pass `extra` are kept. Answer: `{total, rows, mode: "each"}`. `rows` holds up
to 1000 users with their last session (when scanned) and duplicate group.

### `POST /api/bulk/apply`

```json
{"filters": [ ... ], "action": "set_profile", "option": 7, "expected_count": 342}
```

- `expected_count` is the preview's `total`, as confirmed by the person.
- This starts a job. The job previews again, checks the count and the
  sample, and then sends `POST bulkChanges/users/apply` once.
- The rules are in [SAS4_API.md](SAS4_API.md#post-bulkchangesusersapply).

With `extra` set, the body also takes `dry_run` and `workers`. The match is
worked out again and must still equal `expected_count`. Then a per-user job
(`bulk_change_each`) starts. For each user it sends `POST index/user` with
the filter `username = <name>`, which must find exactly that user. Then it
sends `POST bulkChanges/users/apply` with the same filter and the action.
A user whose filter finds anyone else is skipped.

Answer: a [job summary](#job-summary).

## Jobs

### `POST /api/jobs`

```json
{"op": "add_traffic", "params": {"amount": 5, "unit": "GB"},
 "dry_run": true, "workers": 4,
 "source": {"type": "filter", "filters": { ... }, "ids": null}}
```

- `op` is an [operation](#operations) key. `params` holds its form fields.
- `dry_run` defaults to `true`. A dry run sends no write. It builds each
  request body and, for some operations, calls the operation's preview
  route.
- `workers` defaults to 4, and can be 1 to 16 parallel requests.
- `source.type` is one of:
  - `filter`: the users matching `filters`;
  - `managers`: the managers matching `search`;
  - `import`: the rows of `import_id`.
- `ids` limits a `filter` or `managers` source to ticked rows.

The job is refused when:

- a required field is missing from `params` and from some CSV row;
- the operation is unverified, the run is real, and there is more than one
  row, unless the app runs with `--allow-unverified`.

Answer: a [job summary](#job-summary).

### `GET /api/jobs`

This session's jobs: `{"jobs": [summary, ...]}`.

### `GET /api/jobs/{job_id}`

A job summary, with its last 30 row results in `recent`.

### `POST /api/jobs/{job_id}/cancel`

Stops the job. Rows already sent are not undone, and rows not yet started are
marked `cancelled`.

### `POST /api/jobs/{job_id}/retry-failed`

Starts a new real job over the failed rows only, with the same parameters.
The new job has `retry_of` set to the old job's id. Returns 409 while the job
is still running, and 400 for a dry run or when no rows failed.

### `GET /api/jobs/{job_id}/results.csv`

Downloads the job's report. Each row is written to it as it finishes: the
row's identity (line, user or manager, profile, expiration), `action`,
`http_status`, `message`, `transaction_id`, the request body (with passwords
and PINs masked) and a `timestamp`. The report is also kept in
`--reports-dir`.

### Job summary

```json
{"id": 7, "op": "add_traffic", "label": "Add traffic", "dry_run": false,
 "state": "running", "total": 120, "done": 45,
 "counts": {"done": 44, "failed": 1}, "workers": 4,
 "report": "reports/bulk_add_traffic_....csv", "started": 1761400000.1,
 "finished": null, "retry_of": null, "recent": [ ... ], "params": { ... }}
```

- `state` is `queued`, `running`, `done` or `cancelled`.
- Each row's `action` is `done`, `failed`, `skipped`, `would_run` (in a dry
  run) or `cancelled`.

## Operations

These come from `Operations` in `internal/app/operations.go`. `{user_id}` in
a route is filled in from each row.

| Key | Label | Runs on | SAS4 request | Dry-run preview | Verified |
|---|---|---|---|---|---|
| `add_traffic` | Add traffic | users | `POST user/addTraffic` | none | yes |
| `deposit` | Deposit balance | users | `POST user/deposit` | none | yes |
| `withdraw` | Withdraw balance | users | `POST user/withdraw` | none | yes |
| `activate` | Activate / renew | users | `POST user/activate` | `GET user/activationData/{user_id}` | yes |
| `extend` | Extend service | users | `POST user/extend` | `GET user/extensionData/{user_id}` | yes |
| `change_profile` | Change profile | users | `POST user/changeProfile` | none | yes |
| `cancel` | Cancel service (refund) | users | `GET user/refund/{user_id}` | `GET user/refundData/{user_id}` | yes |
| `create` | Create users | CSV import | `POST user` | none | yes |
| `rename` | Rename users | CSV import | `POST user/rename/{user_id}` | none | yes |
| `delete` | Delete users | users | `DELETE user/{user_id}` | none | yes |
| `set_enabled` | Enable / disable | users | `GET user/{user_id}`, then `POST user/{user_id}` | none | no |
| `m_deposit` | Manager deposit | managers | `POST manager/deposit` | none | yes |
| `m_withdraw` | Manager withdraw | managers | `POST manager/withdraw` | none | yes |
| `m_add_points` | Add reward points | managers | `POST manager/addRewardPoints` | none | yes |
| `m_deduct_points` | Deduct reward points | managers | `POST manager/deductRewardPoints` | none | yes |
| `m_pay_debt` | Pay debt | managers | `POST manager/payDebt` | none | no |
| `bulk_change` | Bulk change | the server | `POST bulkChanges/users/apply` | `POST index/user` | yes |

The body each operation sends is shown under its route in
[SAS4_API.md](SAS4_API.md#the-developer-collection).

### Rules every operation follows

- **CSV values win.** When an imported CSV has a non-blank column named like
  a form field (`amount`, `comment`, `profile_id`, ...), that row uses the
  CSV value instead of the form value. So one file can give each user a
  different amount. The fields that can come from the CSV are listed in each
  operation's `csv_columns` in `/api/meta`.
- **Names or ids.** A profile, manager or site can be given by id or by name.
  Names are matched without regard to case.
- **Skipped, not failed.** A row is skipped and written to the report,
  without being sent, when it cannot be sent as it is. For example: the user
  is not in SAS4, the amount is not a number, or the new username is the same
  as the old one.
- **Writes are sent once**, each with a new `transaction_id`.
