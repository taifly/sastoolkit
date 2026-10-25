# SAS4 API

This is a reference for the SAS4 HTTP API, as SAS toolkit uses it. It covers:

1. [Basics](#basics): URLs, encryption, login, paging and errors.
2. [The developer collection](#the-developer-collection): every request in
   the official Postman collection, with a note on whether the app uses it.
3. [Used by the app, not in the collection](#used-by-the-app-not-in-the-collection):
   routes the app calls that the collection does not describe.

Source: the Postman collection "SAS4 for developers",
<https://documenter.getpostman.com/view/11765341/U16byA2y>. Most examples in
the collection are encrypted. They are decrypted here with the default key.

The app's own local API is described in [API.md](API.md).

## Basics

### Base URLs

| Part | Base URL |
|---|---|
| Admin API (managers) | `https://<host>/admin/api/index.php/api/` |
| User portal API (subscribers) | `https://<host>/user/api/index.php/api/` |

Every route below is relative to one of these. The app only uses the admin
API. `--host` and `--scheme` set `<host>` and the scheme
(`sas4.New` in `internal/sas4/client.go`).

### Request bodies are encrypted

Every request body is JSON, encrypted and wrapped as:

```json
{"payload": "<base64>"}
```

The encryption is CryptoJS's default `AES.encrypt(text, passphrase)`:

- AES-256-CBC with PKCS#7 padding.
- Key and IV come from EVP_BytesToKey: MD5, one iteration, and an 8-byte
  random salt.
- The output is the OpenSSL envelope `"Salted__" + salt + ciphertext`, in
  base64. It always starts with `U2FsdGVkX1`.

The passphrase is the same on every SAS4 server. It is `sas4.APISecret` in
`internal/sas4/crypto.go`. You can decrypt a captured payload with:

    echo '<payload>' | openssl enc -d -aes-256-cbc -md md5 -a -A -k '<APISecret>'

Responses are plain JSON, not encrypted.

The collection's examples send the payload form-encoded
(`payload=<base64>`). The app sends it as JSON
(`Content-Type: application/json`). SAS4 accepts both.

### Login and the token

`POST login` returns a JWT in `token`. Send it on every other request:

    Authorization: Bearer <token>

The token expires after about an hour. When any request gets a 401, the
client logs in again once and resends the request. If several workers get a
401 together, only one of them logs in (`Client.relogin`).

The app also sends browser-like `Origin`, `Referer`, `Accept` and
`User-Agent` headers.

### Success and errors

A response counts as a success (`sas4.Succeeded`) when:

- the HTTP status is 2xx;
- the body is a JSON object;
- its `status` field, when there is one, is `200`, `true` or `1`;
- it has no `error` or `errors` field.

SAS4 sometimes answers HTTP 200 with `{"status": 4xx, "message": "..."}`, so
the inner `status` must be checked. Messages are translation keys such as
`rsp_save_success` or `rsp_user_deleted`.

### Paged listings (`index/*`)

`POST index/user`, `index/manager`, `index/online`, `index/UserSessions` and
the other `index/*` routes take:

```json
{"page": 1, "count": 500, "sortBy": "id", "direction": "asc",
 "search": "", "columns": ["id", "username", "..."]}
```

- `columns` chooses the fields in each row. Some fields come back whether
  they are asked for or not (`profile_id`, `enabled`, `profile_details`,
  ...).
- `search` is a substring match over the main text fields.

They answer in the Laravel paginator shape:

```json
{"current_page": 1, "data": [ ... ], "last_page": 12, "total": 5873,
 "per_page": 500, "from": 1, "to": 500, "next_page_url": "...", ...}
```

`Client.Paged` reads pages until `last_page` is reached, or until a page is
shorter than `count`.

### Retries

- Reads (GET, and POST listings) are retried 3 times on network errors and
  5xx, with a back-off.
- Writes are sent once. A write that timed out may already have been
  applied, and sending it again would apply it twice.
- Most money and traffic writes carry a `transaction_id` (a random UUID).
  The app makes a new one for each row and writes it to the job report.

## The developer collection

The collection has 66 requests in 8 folders. The **App** column says which
part of the app uses a request, or `no`. Routes are relative to the base URL.
Hosts and IDs from the examples are replaced by placeholders.

### Authorization

| Method | Route | Name | App |
|---|---|---|---|
| POST | `login` | Access Granted Client Credentials | yes: sign-in |
| GET | `auth` | Get Token Information | no |

**`POST login`**

```json
{"username": "admin", "password": "<password>", "language": "en"}
```

The response is `{"status": 200, "token": "<jwt>"}`.

**`GET auth`** returns the signed-in manager's record in `client`: id,
username, balance, debt limit, ACL group and so on.

### After Authorization Details

| Method | Route | Name | App |
|---|---|---|---|
| GET | `resources/languages` | Available Translations | no |
| GET | `resources/language/{lang}` | Translation File | no |
| GET | `resources/menu` | Menus | no |

- `resources/languages` lists the UI languages (`id`, `name`, `direction`).
- `resources/language/{lang}` returns `{"info": {...}, "words": {key: text}}`.
  Use it to translate the `rsp_*` message keys.
- `resources/menu` lists the menu entries this token may see. Each entry has
  an `acl` permission, for example `prm_users_index`. SAS4 checks
  permissions for each manager, so a route can answer 403 for one manager
  and work for another.

### Dashboard

This folder is empty in the collection.

### User Portal (user panel)

These routes use the user portal base URL `/user/api/index.php/api/` and a
subscriber's login, not a manager's. The app uses none of them.

| Method | Route | Name | Body |
|---|---|---|---|
| POST | `auth/login` | Login | `{"username", "password", "language"}` → `token` |
| POST | `register` | Register | `{"username", "email", "mobile", "password", "confirm_password", "firstname", "lastname"}` |
| POST | `index/invoice` | invoices | `{"page", "count", "sortBy": "id", "direction": "desc"}` |
| POST | `service` | Change Subscription | `{"new_service": "2", "current_password": true}` |
| POST | `index/session` | User sessions | `{"page", "count", "sortBy": "radacctid", "direction": "desc"}` |
| POST | `traffic` | User traffic | `{"report_type": "daily", "month": 9, "year": 2020, "user_id": null}` |
| POST | `redeem` | Redeem code | `{"pin": "136154"}` |
| POST | `user/activate` | Activate user subscription | `{"uuid": "<uuid>", "current_password": true}` |
| POST | `user/extend` | Activate Subscription Extension | `{"profile_id": "3", "current_password": true}` |
| POST | `user` | Change password | `{"key": "password", "value": "<new>", "current_password": true}` |
| GET | `resources/language/{lang}` | Language keywords | none |
| GET | `user` | User Details & Permissions | none |
| GET | `dashboard` | Balance Info | none |
| GET | `service` | Profiles / Services | none |
| GET | `packages` | Packages | none |
| GET | `resources/menu` | Menus | none |
| GET | `extensions/{profile_id}` | Get Extensions | none |

Notes from the collection:

- `user/activate`: the `uuid` stops double clicks from activating twice. The
  UI makes one when it loads and sends the same one on every click.
- `service`: changing to the subscription the user already has returns an
  empty 200.
- `user` (change password) is marked as buggy in the collection.
- `traffic` returns `rx` and `tx` arrays with one value per day.

### Users – List

| Method | Route | Name | App |
|---|---|---|---|
| POST | `index/user` | Users List | yes: loads the user list; Bulk change preview |
| POST | `index/user` | Users List – with search | yes (same route) |
| POST | `user` | User – Create | yes: Create users |
| GET | `user/{user_id}` | User – All Data | yes: Enable / disable reads the user |
| POST | `user/rename/{user_id}` | User – Rename | yes: Rename users |
| GET | `user/activationData/{user_id}` | User – Activation Data | yes: Activate dry run |
| POST | `user/activate` | User – Activation Service | yes: Activate / renew |
| POST | `user/addTraffic` | User – Add Traffic | yes: Add traffic |
| GET | `user/extensionData/{user_id}` | User – Extension Data | yes: Extend dry run |
| GET | `allowedExtensions/{profile_id}` | User – Extension Profiles | yes: the Extend form |
| POST | `user/extend` | User – Extend Service | yes: Extend service |
| POST | `user/changeProfile` | User – Change Profile | yes: Change profile |
| POST | `user/deposit` | User – Deposit | yes: Deposit balance |
| POST | `user/withdraw` | User – Withdraw | yes: Withdraw balance |
| GET | `user/refundData/{user_id}` | User – Cancel Data | yes: Cancel dry run |
| GET | `user/refund/{user_id}` | User – Cancel Service | yes: Cancel service (refund) |
| DELETE | `user/{user_id}` | User – Delete | yes: Delete users |

**`POST index/user`**: a [paged listing](#paged-listings-index). The
collection lists these columns: `n_row`, `id`, `username`, `firstname`,
`lastname`, `expiration`, `parent_username`, `name` (the profile name),
`balance`, `traffic`, `city`, `static_ip`, `notes`, `last_online`,
`company`, `simultaneous_sessions`, `used_traffic`, `phone`, `address`,
`contract_id`.

The app asks for the same columns as the admin GUI's user list
(`UserColumns` in `internal/app/users.go`). These include `loan_balance`,
`group_name`, `remaining_days`, `created_at`, `available_traffic`,
`national_id`, `mikrotik_ipv6_prefix`, `site_name`, `pin_tries` and
`debt_days`. It reads 500 rows a page (`--page-size`), sorted by `id`. For
`bulk_filter`, see [Bulk change](#post-bulkchangesusersapply).

**`POST user`** (create). The collection's example:

```json
{"username": "test", "enabled": 1, "password": "<pw>", "confirm_password": "<pw>",
 "profile_id": 1, "parent_id": 2, "site_id": 0, "mac_auth": 0, "allowed_macs": null,
 "firstname": "Test", "lastname": "API", "company": "...", "email": "...",
 "phone": null, "city": null, "address": null, "apartment": null, "street": null,
 "contract_id": null, "national_id": null, "notes": null,
 "expiration": "2020-07-15 15:04:24", "simultaneous_sessions": 1, "static_ip": null,
 "mikrotik_winbox_group": null, "mikrotik_framed_route": null,
 "mikrotik_addresslist": null, "mikrotik_ipv6_prefix": null,
 "auto_renew": 0, "user_type": "0"}
```

Required: `username`, `enabled`, `password`, `profile_id`, `parent_id` (the
owner manager) and `site_id`.

The app sends `username`, `enabled`, `password`, `confirm_password`,
`profile_id`, `parent_id` and `site_id`. It adds `firstname`, `lastname`,
`phone`, `email`, `address`, `city`, `company`, `notes`, `contract_id`,
`national_id`, `static_ip` and `expiration` when the CSV has them
(`buildCreate`).

**`GET user/{user_id}`** returns the full record in `data`: profile,
enabled, expiration, contact fields, parent, MAC settings and so on.

**`POST user/rename/{user_id}`**

```json
{"new_username": "new-name"}
```

**`POST user/activate`**: renews the user on their current profile and
charges for it.

```json
{"method": "credit", "pin": "", "user_id": "3", "money_collected": true,
 "comments": "...", "issue_invoice": true, "transaction_id": "<uuid>"}
```

- `method` is `credit` (the manager's balance), `reward_points` or `card`.
- `card` needs a `pin`.
- `user_id` is a string.
- `GET user/activationData/{user_id}` shows the price, the balances and the
  new expiration before you activate. The app's dry run calls it.

**`POST user/addTraffic`**

```json
{"user_id": 1, "username": "name", "amount": 1024, "comment": "",
 "transaction_id": "<uuid>", "target": "rxtx_mbytes"}
```

- `amount` is in MB. The app converts GB and TB to MB.
- `target` is `rxtx_mbytes` (download + upload), `rx_mbytes` (download only)
  or `tx_mbytes` (upload only).

**`POST user/extend`**: adds an extension profile on top of the current
service.

```json
{"user_id": "2", "profile_id": "2", "method": "reward_points", "transaction_id": "<uuid>"}
```

- `profile_id` is the extension's id, from `GET allowedExtensions/{profile_id}`
  (keyed by the user's current profile).
- `method` is `credit` or `reward_points`.
- `GET user/extensionData/{user_id}` shows the remaining traffic and the
  balance. The app's dry run calls it.

**`POST user/changeProfile`**: an active user moves to the new profile on
their next expiration date.

The collection's description shows:

```json
{"user_id": "2", "profile_id": "2", "method": "reward_points", "transaction_id": "<uuid>"}
```

Its encrypted example decrypts to something else:

```json
{"user_id": "2", "profile_id": 4, "change_type": "schedule"}
```

The app sends the first shape, with `method` set to `credit` or
`reward_points`.

**`POST user/deposit`** and **`POST user/withdraw`**: these move money
between the manager's balance and the user's balance.

```json
{"user_id": 2, "user_username": "name", "amount": 10, "comment": "",
 "transaction_id": "<uuid>"}
```

A successful withdraw answers `rsp_amount_deducted`.

**`GET user/refund/{user_id}`**: cancels the current service and refunds the
manager. This is a GET with no body, even though it changes data.
`GET user/refundData/{user_id}` shows the refund amount first. The app's dry
run calls it.

**`DELETE user/{user_id}`** answers `rsp_user_deleted`.

### Users – Form

| Method | Route | Name | App |
|---|---|---|---|
| GET | `user/overview/{user_id}` | User – Overview | no |
| GET | `mac/{user_id}` | User – MAC | no |
| GET | `customRadiusAttribute/user/{user_id}` | User – Custom Radius Attribute | no |
| GET | `site` | User – Site | yes: the site list for Create users |
| GET | `list/profile/{id}` | User – List Profiles | yes, as `list/profile/0` (see Profiles) |
| POST | `index/UserHistory/{user_id}` | User – History | no |
| POST | `index/UserJournal/{user_id}` | User – Journal | no |
| POST | `userNetworksTraffic` | User – Network Traffic | no |
| POST | `user/traffic` | User – Traffic | no |

- `user/overview/{user_id}` returns the user's summary page: profile, status,
  balance, remaining traffic and contact fields. It includes the password in
  plain text.
- `site` returns `{"data": [{"id": 0, "name": "_default", "status": 1}]}`.
- `index/UserHistory/{user_id}` is a paged listing of events, with
  `columns: ["created_at", "event", "description", "username"]` and
  `sortBy: "created_at"`.
- `index/UserJournal/{user_id}` is a paged listing of the money journal, with
  `columns: ["created_at", "cr", "dr", "amount", "balance", "operation", "description"]`.
- `user/traffic` takes `{"report_type": "daily", "month": 7, "year": 2020, "user_id": "2"}`
  and returns `rx` and `tx` arrays.
- `userNetworksTraffic` takes the same body plus `"zone_id": -1`.

### Users – Online List

| Method | Route | Name | App |
|---|---|---|---|
| POST | `index/online` | Online Users List | no |
| POST | `index/online` | Online Users List – with search | no |
| POST | `user/ping` | Ping User | no |

- `index/online` is a paged listing of the open RADIUS sessions. Example
  `columns`: `id`, `username`, `acctoutputoctets`, `acctinputoctets`,
  `user_profile_name`, `framedipaddress`, `callingstationid`,
  `acctsessiontime`, `oui`.
- `user/ping` takes `{"radacctid": 1005910, "username": "name"}`.

The app does not call `index/online`. It takes the online state from each
row's `online_status` field in `index/user`.

### Managers

| Method | Route | Name | App |
|---|---|---|---|
| GET | `index/manager` | Managers – Simple List | yes: fallback for the manager list |
| GET | `manager/tree` | Managers – Tree | no |
| POST | `index/manager` | Managers – List | yes: loads the manager list |
| POST | `manager` | Add Manager | no |
| DELETE | `manager/{manager_id}` | Delete Manager | no |
| POST | `manager/{manager_id}` | Update Manager | no |
| POST | `manager/{manager_id}` | Rename Manager | no |
| POST | `manager/deposit` | Manager Deposit | yes: Manager deposit |
| POST | `manager/deposit` | Manager – Deposit Loans | yes: Manager deposit, "As a loan" |
| POST | `manager/withdraw` | Managers – withdraw | yes: Manager withdraw |
| GET | (no URL) | Managers – Add Reward Points | no; an empty copy of the next request |
| POST | `manager/addRewardPoints` | Managers – Add Reward Points | yes: Add reward points |
| POST | `manager/deductRewardPoints` | Managers – Deduct Managers points | yes: Deduct reward points |
| POST | `manager/payDebt` | Manager – Pay debt | yes: Pay debt |

**`POST index/manager`**: a paged listing. The collection uses these
columns: `username`, `firstname`, `lastname`, `balance`, `name`, `city`,
`users_count`, `reward_points`. The app asks for `ManagerColumns`: `id`,
`username`, `firstname`, `lastname`, `balance`, `loan_balance`, `name`,
`users_count`.

If the paged listing fails, the app falls back to `GET index/manager`, which
returns `{"data": [{"id": 1, "username": "admin"}, ...]}`.

**`GET manager/tree`** returns the manager hierarchy as
`[{"id", "parent_id", "username"}]`.

**`POST manager`** (add) and **`POST manager/{manager_id}`** (update) take
the full manager record:

```json
{"username": "example_manager", "enabled": 1, "password": "<pw>", "confirm_password": "<pw>",
 "acl_group_id": 21, "parent_id": 1, "firstname": "...", "lastname": "...",
 "company": null, "email": null, "phone": null, "city": null, "address": null, "notes": null,
 "subscriber_prefix": null, "subscriber_suffix": null, "max_users": "0", "site_id": null,
 "debt_limit": "0", "discount_rate": "0", "mikrotik_addresslist": null,
 "allowed_ppp_services": null, "allowed_nases": [], "requires_2fa": 0, "admin_notes": null,
 "limit_delete": 0, "limit_delete_count": "0", "limit_rename": 0, "limit_rename_count": "0",
 "limit_profile_change": 0, "limit_profile_change_count": "0",
 "limit_mac_change": 0, "limit_mac_change_count": "0"}
```

- On update, `username` is left out and `password: null` keeps the current
  password.
- Rename is the same route with only `{"username": "new-name"}`.

**`POST manager/deposit`**

```json
{"manager_id": 3, "manager_username": "Manager_2", "amount": 24, "comment": "",
 "transaction_id": "<uuid>", "is_loan": false, "balance": null}
```

With `is_loan: true`, the deposit is recorded as a loan (the manager's
`loan_balance`). The app sends every field except `balance`.

The collection's "Manager Deposit" description shows a listing body by
mistake. Its encrypted example is the deposit body above.

**`POST manager/withdraw`**

```json
{"manager_id": 2, "manager_username": "name", "amount": 24, "comment": "",
 "transaction_id": "<uuid>", "balance": 985}
```

The app sends every field except `balance`, which is the GUI's display
value.

**`POST manager/addRewardPoints`** and **`POST manager/deductRewardPoints`**

```json
{"manager_id": 2, "amount": 23}
```

`amount` is a whole number of points.

**`POST manager/payDebt`**: pays off the manager's debt. The collection's
description has no body, but its encrypted example decrypts to:

```json
{"manager_id": 2, "manager_username": "Manager_1", "amount": 55, "comment": "",
 "transaction_id": null, "is_loan": false, "debt_for_me": 111, "debt": 111}
```

The app sends the deposit shape: `manager_id`, `manager_username`, `amount`,
`comment` and `transaction_id`. It does not send `is_loan`, `debt_for_me` or
`debt`. That is why the operation is marked unverified (see
[API.md](API.md#operations)).

**`DELETE manager/{manager_id}`** has no body.

### Profiles

| Method | Route | Name | App |
|---|---|---|---|
| GET | `list/profile/0` | Profiles – Simple List | yes: profile names and pickers, loaded at sign-in |

The response is `{"status": 200, "data": [{"id": 1, "name": "default-2Mbit-1Month"}, ...]}`.
Sign-in fails if this request fails, because every user row needs its
profile name.

## Used by the app, not in the collection

The app also calls these routes. The collection does not describe them. Their
shapes come from requests the admin GUI sends on bss.libonet.ly, captured
and decrypted.

### `POST bulkChanges/users/apply`

This is SAS4's own Bulk Changes: one action, applied by the server to every
user matching a filter, in one request (`internal/app/bulkchange.go`).

```json
{"filter": [{"key": "profile_id", "op": "=", "value": 14,
             "value_label": "flat-Old", "id": "<uuid>"}],
 "action": {"name": "set_traffic_balance", "option": 92160}}
```

- `filter` rows are joined with AND.
- `op` is one of `=`, `!=`, `<`, `>` or `like`.
- `value` is the id for profile, manager and site keys, a number for number
  and yes/no keys (`1`/`0`), and text otherwise. The display name goes in
  `value_label`. `id` is a random UUID for each row.
- `action.option` holds the action's value. Actions without a value send only
  `name`.

Filter keys (`filterKeys`):

| Key | Type |
|---|---|
| `username`, `firstname`, `lastname`, `email`, `company`, `phone`, `city`, `address`, `contract_id`, `national_id`, `static_ip` | text |
| `expiration`, `creation_date` | date |
| `enabled`, `auto_renew` | yes/no |
| `profile_id` | profile id |
| `parent_id`, `parent_and_subs` | manager id |
| `site` | site id |
| `simultaneous_sessions`, `user_type`, `group` | number |

Actions (`bulkActions`):

| Action | Option |
|---|---|
| `add_traffic`, `add_traffic_dl`, `add_traffic_ul`, `set_traffic_balance` | MB |
| `add_days`, `add_hours` | number |
| `set_expiration` | text (date) |
| `set_profile` | profile id |
| `set_site` | site id |
| `set_parent` | manager id |
| `set_group` | group id |
| `set_city` | text |
| `set_simultaneous_sessions` | number |
| `send_sms` | text |
| `enable`, `disable`, `set_auto_renew`, `unset_auto_renew`, `disconnect`, `delete` | none |

The GUI also has an `export` action. The app leaves it out, because it
answers with a spreadsheet and changes nothing.

**Preview.** `POST index/user` takes the same filter as `bulk_filter`, with
`count: 20`. Its `total` is the number of users the action will reach.

**Safety checks before sending.** An empty filter means every user, so the
app refuses to send one. Just before sending, it runs the preview again and
refuses when:

- the count is different from the one that was confirmed;
- any sample row does not match the filter, which would mean SAS4 ignored
  `bulk_filter`.

The request is sent once and never retried.

### `POST index/UserSessions`

The RADIUS accounting history: one row per session, open or closed. It is a
paged listing. The app sends the same columns as the admin GUI's Sessions
report (`SessionColumns`): `username`, `acctstarttime`, `acctstoptime`,
`framedipaddress`, `nasipaddress`, `callingstationid`, `acctinputoctets`,
`acctoutputoctets`, `calledstationid`, `nasportid` and
`acctterminatecause`.

- **Last-session scan** (`internal/app/lastsession.go`): reads every page,
  `sortBy: "acctstarttime"`, `direction: "desc"`. It stops when every loaded
  user has been seen, when it reaches the look-back limit, or at the end of
  the history.
- **One user's history**: `search` is set to the username, 50 rows a page.
  `search` is a substring match (`ali` also finds `ali2`), so the app keeps
  only exact username matches.

### `POST user/{user_id}` (update user)

This is used by **Enable / disable**. The collection documents an update for
managers (`POST manager/{manager_id}`), but none for users. The app does the
same thing for a user:

1. It reads the user with `GET user/{user_id}`.
2. It copies every field that is not an object or a list.
3. It sets `enabled` to `1` or `0`, and posts the record back to
   `user/{user_id}`.

When the user already has the wanted state, the row is skipped. This
operation is unverified: with more than one row, it only runs for real when
the app is started with `--allow-unverified`.

### `columns` values

The `columns` lists the app sends to `index/user` and `index/manager` are
longer than the collection's. They are taken from the admin GUI
(`UserColumns` and `ManagerColumns` in `internal/app/users.go`).
