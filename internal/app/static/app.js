"use strict";

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

// Build DOM without innerHTML: every value from SAS4 goes in as text.
function h(tag, attrs = {}, ...children) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "checked" || k === "selected" || k === "disabled") el[k] = !!v;
    else if (k === "value") el.value = v;
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) {
    if (c === null || c === undefined || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

async function api(path, opts = {}) {
  const init = { method: opts.method || (opts.body ? "POST" : "GET"),
                 headers: {}, credentials: "same-origin" };
  if (opts.form) init.body = opts.form;
  else if (opts.body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(opts.body);
  }
  const resp = await fetch(path, init);
  let data = null;
  try { data = await resp.json(); } catch (_) { /* empty body */ }
  if (resp.status === 401 && path !== "/api/login") { showLogin(); }
  if (!resp.ok) {
    const detail = data && data.detail;
    throw new Error(typeof detail === "string" ? detail
      : detail ? JSON.stringify(detail) : `HTTP ${resp.status}`);
  }
  return data;
}

function debounce(fn, ms) {
  let t;
  return (...args) => { clearTimeout(t); t = setTimeout(() => fn(...args), ms); };
}

const fmt = n => (n === null || n === undefined) ? "" : Number(n).toLocaleString();

// ---------------------------------------------------------------------------
// user filter panel
// ---------------------------------------------------------------------------

// One panel, used by every page that picks users (Select users, Bulk change,
// both reports). value() is the API's `filters` object (docs/API.md). The
// session part reads each user's last session, so it needs a session scan;
// the server answers 409 "scan the sessions first" without one.

const TRAFFIC_UNITS = { MB: 1024 ** 2, GB: 1024 ** 3 };

class FilterPanel {
  constructor(root, { onChange } = {}) {
    this.root = root;
    this.profiles = new Set();
    this.changed = debounce(() => (onChange || (() => {}))(), 300);
    this.build();
  }

  checks(values) {
    return h("div", { class: "checks" }, values.map(([v, label, title]) =>
      h("label", { title: title || null },
        h("input", { type: "checkbox", value: v, onchange: () => this.changed() }), " " + label)));
  }

  field(label, ...inputs) {
    return h("div", { class: "field" }, h("span", {}, label), ...inputs);
  }

  input(attrs) {
    return h("input", { ...attrs, oninput: () => this.changed() });
  }

  range(name) {
    return h("div", { class: "range" },
      this.el[name + "_min"] = this.input({ type: "number", min: 0, step: "any", placeholder: "min" }),
      h("span", { class: "muted" }, "–"),
      this.el[name + "_max"] = this.input({ type: "number", min: 0, step: "any", placeholder: "max" }));
  }

  build() {
    const el = this.el = {};
    el.profileSearch = h("input", { type: "search", placeholder: "Search profiles…",
                                    oninput: () => this.renderProfiles() });
    el.profileList = h("div", { class: "picker-list" });
    el.manager = h("select", { onchange: () => this.changed() });
    el.statuses = this.checks([["active", "Active"], ["expired", "Expired"],
                               ["disabled", "Disabled"], ["online", "Online"]]);
    el.search = this.input({ type: "search", placeholder: "username, name, phone" });
    el.from = this.input({ type: "date" });
    el.to = this.input({ type: "date" });

    el.states = this.checks([["online", "Online"], ["offline", "Offline"],
                             ["none", "No session", "No session in the scan's look-back window"]]);
    el.dups = this.checks([["session_mac", "Session MAC", "Same MAC on the last session"],
                           ["session_ip", "Session IP", "Same IP on the current session (online users only)"],
                           ["static_ip", "Static IP", "Same static IP on the user record"]]);
    el.mac = this.input({ type: "search", placeholder: "contains, e.g. aabbcc" });
    el.nas = this.input({ type: "search", placeholder: "contains" });
    el.reason = h("select", { onchange: () => this.changed() }, h("option", { value: "" }, "Any"));
    el.unit = h("select", { onchange: () => this.changed() },
      Object.keys(TRAFFIC_UNITS).map(u => h("option", { value: u, selected: u === "GB" }, u)));
    el.session = h("details", { class: "session-filters" },
      h("summary", {}, "Session filters (last session)"),
      h("div", { class: "filters" },
        this.field("Session", el.states),
        this.field("Shared with another user", el.dups),
        this.field("MAC", el.mac),
        this.field("NAS IP", el.nas),
        this.field("Stop reason", el.reason),
        this.field("Traffic unit", el.unit),
        this.field("Upload", this.range("upload")),
        this.field("Download", this.range("download")),
        this.field("Total", this.range("total"))),
      h("p", { class: "muted small" },
        "Matched against each user's last session — these need a session scan (Static IP does not)."));

    this.root.replaceChildren(
      h("div", { class: "filters" },
        h("div", { class: "field wide" }, h("span", {}, "Profiles"),
          h("div", { class: "picker" }, el.profileSearch, el.profileList)),
        this.field("Manager", el.manager),
        this.field("Status", el.statuses),
        this.field("Search", el.search),
        this.field("Expires from", el.from),
        this.field("Expires to", el.to)),
      el.session);
    this.refresh();
  }

  renderProfiles() {
    const needle = this.el.profileSearch.value.trim().toLowerCase();
    const profiles = (S.meta && S.meta.profiles) || [];
    this.el.profileList.replaceChildren(...profiles
      .filter(p => !needle || String(p.name).toLowerCase().includes(needle) || String(p.id) === needle)
      .map(p => h("label", {},
        h("input", { type: "checkbox", checked: this.profiles.has(p.id), onchange: e => {
          e.target.checked ? this.profiles.add(p.id) : this.profiles.delete(p.id);
          this.changed();
        } }),
        h("span", {}, p.name), h("span", { class: "count" }, `#${p.id}`))));
  }

  // Rebuilds the choices from S.meta (and the stop reasons from the last
  // scan), keeping what is picked.
  refresh(stopReasons) {
    this.renderProfiles();
    const sel = this.el.manager, keep = sel.value;
    sel.replaceChildren(h("option", { value: "" }, "Any manager"),
      ...((S.meta && S.meta.managers) || []).map(m => h("option", { value: m.name, selected: m.name === keep }, m.name)));
    if (stopReasons) {
      const r = this.el.reason, keepR = r.value;
      const list = keepR && !stopReasons.includes(keepR) ? [...stopReasons, keepR] : stopReasons;
      r.replaceChildren(h("option", { value: "" }, "Any"),
        ...list.map(v => h("option", { value: v, selected: v === keepR }, v)));
    }
  }

  reset() {
    this.profiles.clear();
    for (const i of $$("input", this.root)) {
      if (i.type === "checkbox") i.checked = false; else i.value = "";
    }
    this.el.manager.value = this.el.reason.value = "";
    this.el.session.open = false;
    this.refresh([]);
  }

  value() {
    const el = this.el, picked = box => $$("input:checked", box).map(i => i.value);
    const unit = TRAFFIC_UNITS[el.unit.value];
    const bytes = input => input.value === "" ? null : Number(input.value) * unit;
    const out = {
      profiles: [...this.profiles],
      managers: el.manager.value ? [el.manager.value] : [],
      statuses: picked(el.statuses),
      search: el.search.value.trim(),
      expires_from: el.from.value || null,
      expires_to: el.to.value || null,
      duplicates: picked(el.dups),
      session_states: picked(el.states),
      mac: el.mac.value.trim(),
      nas_ip: el.nas.value.trim(),
      stop_reasons: el.reason.value ? [el.reason.value] : [],
    };
    for (const k of ["upload", "download", "total"]) {
      out[k + "_min"] = bytes(el[k + "_min"]);
      out[k + "_max"] = bytes(el[k + "_max"]);
    }
    return out;
  }

  active() {
    const v = this.value();
    return Boolean(v.profiles.length || v.managers.length || v.statuses.length || v.search ||
      v.expires_from || v.expires_to || v.duplicates.length || readsSessions(v));
  }

  // A short description of what is set, for confirm dialogs.
  describe() {
    const v = this.value(), out = [];
    const names = (S.meta && S.meta.profiles) || [];
    if (v.profiles.length) out.push("profile " + v.profiles.map(id => (names.find(p => p.id === id) || {}).name || id).join(" or "));
    if (v.managers.length) out.push("manager " + v.managers[0]);
    if (v.statuses.length) out.push("status " + v.statuses.join(" or "));
    if (v.search) out.push(`search "${v.search}"`);
    if (v.expires_from || v.expires_to) out.push(`expires ${v.expires_from || "…"} to ${v.expires_to || "…"}`);
    if (v.session_states.length) out.push("session " + v.session_states.join(" or "));
    if (v.duplicates.length) out.push("shares " + v.duplicates.join(" or ") + " with another user");
    if (v.mac) out.push(`MAC contains ${v.mac}`);
    if (v.nas_ip) out.push(`NAS IP contains ${v.nas_ip}`);
    if (v.stop_reasons.length) out.push("stop reason " + v.stop_reasons[0]);
    for (const k of ["upload", "download", "total"]) {
      const lo = this.el[k + "_min"].value, hi = this.el[k + "_max"].value;
      if (lo || hi) out.push(`${k} ${lo || "0"}–${hi || "∞"} ${this.el.unit.value}`);
    }
    return out;
  }
}

// Whether filters read the last session (static IP alone does not).
function readsSessions(v) {
  return Boolean(v.duplicates.some(d => d !== "static_ip") || v.session_states.length ||
    v.mac || v.nas_ip || v.stop_reasons.length ||
    ["upload", "download", "total"].some(k => v[k + "_min"] !== null || v[k + "_max"] !== null));
}

// Stop reasons of the last scan, for every panel's dropdown.
async function refreshPanels() {
  let reasons;
  try { reasons = (await api("/api/sessions/status")).stop_reasons; } catch (_) { /* not signed in */ }
  for (const p of Object.values(PANELS)) p.refresh(reasons);
}

// ---------------------------------------------------------------------------
// state
// ---------------------------------------------------------------------------

const S = {
  meta: null,
  op: null,
  tab: "filter",
  page: 1,
  pageSize: 100,
  total: 0,
  ticked: new Set(),
  rows: [],               // every row loaded so far, in table order
  loading: false,
  querySeq: 0,
  partial: false,         // the directory is still loading
  profileCounts: {},
  imported: null,         // {import_id, matched, target}
  dirReady: false,
  openJobs: new Set(),
  jobsTimer: null,
  extensions: null,
};

// One filter panel per page; see FilterPanel.
const PANELS = {
  ops: new FilterPanel($("#user-filters"), { onChange: () => {
    S.ticked.clear();
    S.extensions = null;
    renderForm();
    query();
  } }),
  bulk: new FilterPanel($("#bulk-filters"), { onChange: () => bulkFiltersChanged() }),
  activity: new FilterPanel($("#r-filters"), { onChange: () => { if (S.report) runReport(); } }),
  sessions: new FilterPanel($("#ls-filters"), { onChange: () => requerySessions() }),
};

// ---------------------------------------------------------------------------
// session
// ---------------------------------------------------------------------------

async function boot() {
  const info = await api("/api/session");
  S.session = info;
  $("#host").textContent = info.host;
  setupUrlField(info);
  if (info.logged_in) startApp(); else showLogin();
}

function showLogin() {
  $("#app-view").hidden = true;
  $("#reports-view").hidden = true;
  $("#views").hidden = true;
  $("#login-view").hidden = false;
  for (const id of ["#logout", "#dir-status", "#dir-refresh"]) $(id).hidden = true;
  $("#who").textContent = "";
}

// The system URL: prefilled with the last one used in this browser, else the
// app's default (--host). Locked when the app only allows its default.
const URL_STORE = "sastoolkit.url";

function setupUrlField(info) {
  const input = $("#login-url"), hint = $("#login-url-hint");
  let last = null;
  try { last = localStorage.getItem(URL_STORE); } catch (_) { /* storage blocked */ }
  if (!info.allow_url_change) {
    input.value = info.default_host;
    input.readOnly = true;
    hint.textContent = "This app is locked to this system.";
  } else {
    input.value = last || info.default_host;
    input.readOnly = false;
    hint.textContent = "The address you open SAS4 admin at, e.g. https://sas.example.com";
  }
}

$("#login-form").addEventListener("submit", async e => {
  e.preventDefault();
  const form = e.target, err = $("#login-error"), btn = $("button", form);
  err.hidden = true; btn.disabled = true; btn.textContent = "Signing in…";
  try {
    const r = await api("/api/login", { body: { username: form.username.value,
                                                password: form.password.value,
                                                url: form.url.value } });
    form.password.value = "";
    form.url.value = r.host;                     // show it as it was understood
    try { localStorage.setItem(URL_STORE, r.host); } catch (_) { /* ignore */ }
    $("#host").textContent = r.host;
    await startApp();
  } catch (ex) {
    err.textContent = ex.message; err.hidden = false;
  } finally {
    btn.disabled = false; btn.textContent = "Sign in";
  }
});

$("#logout").addEventListener("click", async () => {
  await api("/api/logout", { method: "POST" });
  // The next sign-in may be to another system: nothing from this one
  // (ticked ids, chosen profiles, an uploaded CSV) may carry over.
  S.meta = null;
  S.dirReady = false;
  S.ticked.clear();
  for (const p of Object.values(PANELS)) p.reset();
  S.imported = null;
  S.rows = [];
  S.fields = [];
  S.extensions = null;
  S.bulkPreview = null;
  S.report = null;
  $("#r-body").hidden = true;
  S.ls.data = null;
  S.ls.page = 1;
  $("#ls-body").hidden = true;
  $("#import-result").replaceChildren();
  $("#bulk-result").replaceChildren();
  $("#jobs").replaceChildren(h("p", { class: "muted" }, "No jobs yet."));
  showLogin();
});

async function startApp() {
  S.meta = await api("/api/meta");
  $("#host").textContent = S.meta.host;
  $("#login-view").hidden = true;
  $("#views").hidden = false;
  showView(S.view || "ops");
  $("#logout").hidden = false;
  $("#dir-status").hidden = false;
  $("#who").textContent = S.meta.username;
  renderOps();
  refreshPanels();
  renderColumnPicker();
  selectOp(S.op ? S.op.key : S.meta.operations[0].key);
  pollDirectory();
  refreshJobs();
}

// ---------------------------------------------------------------------------
// directory loading
// ---------------------------------------------------------------------------

async function pollDirectory() {
  let status;
  try { status = await api("/api/directory"); } catch (_) { return; }
  if (status.fields && status.fields.join() !== S.fields.join()) {
    S.fields = status.fields;
    renderColumnPicker();
  }
  const el = $("#dir-status");
  $("#dir-refresh").hidden = status.state === "loading";
  renderProgress(status);
  if (status.state === "loading") {
    const wasReady = S.dirReady;
    S.dirReady = false;
    el.textContent = `Loading users… ${fmt(status.loaded)}` +
      (status.total ? ` / ${fmt(status.total)}` : "");
    // Show what has arrived. Re-query only while the person is still near
    // the top, so a refresh never yanks the table out from under a scroll.
    if (status.browsable && (wasReady || !S.rows.length || S.rows.length <= S.pageSize)) {
      S.partial = true;
      query();
    } else if (!status.browsable) {
      renderRowsMessage("Fetching users from SAS4…");
    }
    updateSummary();
    setTimeout(pollDirectory, 1000);
  } else if (status.state === "error") {
    el.textContent = "Loading failed";
    S.dirReady = false;
    renderRowsMessage("Loading the user list failed: " + status.error +
                      " — click Refresh list to try again.");
    $("#dir-refresh").hidden = false;
  } else if (status.state === "ready") {
    const wasReady = S.dirReady;
    S.dirReady = true;
    S.partial = false;
    el.textContent = `${fmt(status.users)} users · ${fmt(status.managers)} managers · ${status.loaded_at}`;
    if (status.manager_error) el.title = status.manager_error;
    if (!wasReady) {
      S.meta = await api("/api/meta");        // managers are known now
      refreshPanels();
      if (S.view === "reports") showReport(S.reportTab || "activity");
      renderForm();
      query();
    }
  } else {
    setTimeout(pollDirectory, 1000);
  }
}

// Thin bar under the top bar plus a panel above the table, both fed by the
// directory status. Until SAS4 reports a total the panel bar just animates.
function renderProgress(status) {
  const loading = status.state === "loading";
  $("#loadbar").hidden = !loading;
  $("#load-panel").hidden = !loading;
  if (!loading) return;
  const { loaded = 0, total, started } = status;
  const pct = total ? Math.min(100, (100 * loaded) / total) : 0;
  $("#loadbar > div").style.width = `${pct}%`;
  $("#load-fill").style.width = `${pct}%`;
  $(".big-bar").classList.toggle("indeterminate", !total);
  $("#load-count").textContent = total
    ? `Fetching users from SAS4: ${fmt(loaded)} / ${fmt(total)} (${Math.floor(pct)}%)`
    : `Fetching users from SAS4: ${fmt(loaded)}`;
  let eta = "";
  if (total && started && loaded > 0) {
    const elapsed = Date.now() / 1000 - started;
    const left = (elapsed / loaded) * (total - loaded);
    eta = left < 60 ? `about ${Math.max(1, Math.round(left))} s left`
                    : `about ${Math.round(left / 60)} min left`;
  }
  $("#load-eta").textContent = eta + (status.browsable ? " · you can browse the users loaded so far" : "");
}

$("#dir-refresh").addEventListener("click", async () => {
  await api("/api/directory/refresh", { method: "POST" });
  S.dirReady = false;
  pollDirectory();
});

// ---------------------------------------------------------------------------
// operations
// ---------------------------------------------------------------------------

function renderOps() {
  const nav = $("#ops");
  nav.replaceChildren();
  let group = null;
  for (const op of S.meta.operations) {
    if (op.group !== group) {
      group = op.group;
      nav.append(h("div", { class: "group" }, group));
    }
    nav.append(h("button", { "data-op": op.key, onclick: () => selectOp(op.key) },
      h("span", {}, op.label),
      op.danger ? h("span", { class: "badge", title: "Destructive" }, "!") : null,
      !op.verified ? h("span", { class: "badge", title: "Not in the API docs" }, "?") : null));
  }
  // Shortcuts into the Reports view.
  nav.append(h("div", { class: "group" }, "Reports"));
  for (const [name, label] of [["activity", "Users over time"], ["sessions", "Last session"]]) {
    nav.append(h("button", { "data-report": name, onclick: () => { S.reportTab = name; showView("reports"); } },
      h("span", {}, label)));
  }
}

function selectOp(key) {
  const op = S.meta.operations.find(o => o.key === key);
  const targetChanged = !S.op || S.op.target !== op.target;
  S.op = op;
  $$("#ops button").forEach(b => b.classList.toggle("active", b.dataset.op === key));
  $("#op-title").textContent = op.label;
  $("#op-help").textContent = op.help || "";
  const warn = $("#op-warn");
  warn.hidden = op.verified;
  warn.textContent = op.verified ? "" :
    "This request is not in the SAS4 API docs. Run it live on ONE row first and check the result in SAS4." +
    (S.meta.allow_unverified ? "" : " Bulk runs are blocked until the app is started with --allow-unverified.");

  const isManagers = op.target === "managers", isNew = op.target === "new_users";
  const isBulk = op.target === "server_bulk";
  $("#select-title").textContent = isBulk ? "Users matching" : isManagers ? "Select managers"
    : isNew ? "Users to create" : "Select users";
  $("#user-filters").hidden = isManagers;
  $("#manager-filters").hidden = !isManagers;
  $("#colpick").hidden = isManagers;
  $("#exportpick").hidden = isManagers;
  $('[data-tab="filter"]').disabled = isNew;
  $("#tabs").hidden = isBulk;
  $("#run-opts").hidden = isBulk && !PANELS.bulk.active();
  if (isBulk) {
    S.bulkPreview = null;
    renderBulkRows();
    $("#bulk-result").replaceChildren();
    $("#tab-filter").hidden = $("#tab-import").hidden = true;
    $("#tab-bulk").hidden = false;
    renderForm();
    updateSummary();
    return;
  }
  $("#tab-bulk").hidden = true;
  if (targetChanged) {
    S.ticked.clear();
    S.imported = null;
    $("#import-result").replaceChildren();
  }
  const cols = op.csv_columns.length ? op.csv_columns : [isManagers ? "manager_username" : "username"];
  $("#import-help").textContent =
    `CSV with a header row. Columns: ${cols.join(", ")}. ` +
    (isNew ? "Rows whose username already exists are skipped."
           : "Rows are matched to SAS4 by username; a column named like a form field overrides it for that row.");
  setTab(isNew ? "import" : (S.tab === "import" && !targetChanged ? "import" : "filter"));
  renderForm();
  if (targetChanged && S.dirReady && !isNew) query();
  updateSummary();
}

function renderForm() {
  const op = S.op, form = $("#op-form");
  if (!op) return;
  const old = readParams();
  form.replaceChildren();
  if (op.target === "server_bulk") { renderBulkAction(form, old); return; }
  for (const f of op.fields) {
    const current = old[f.name] !== undefined ? old[f.name] : f.default;
    let input;
    if (f.type === "checkbox") {
      input = h("input", { type: "checkbox", name: f.name, checked: !!current });
      form.append(h("label", { class: "check" }, input, f.label));
      continue;
    }
    if (f.type === "select") {
      input = h("select", { name: f.name },
        f.options.map(([v, l]) => h("option", { value: v, selected: String(current) === String(v) }, l)));
    } else if (f.type === "profile" || f.type === "manager" || f.type === "site") {
      const items = f.type === "profile" ? S.meta.profiles
                  : f.type === "manager" ? S.meta.managers : S.meta.sites;
      input = h("select", { name: f.name },
        h("option", { value: "" }, f.required ? "— choose —" : "— none —"),
        items.map(i => h("option", { value: i.id, selected: String(current) === String(i.id) },
                         `${i.name} (#${i.id})`)));
    } else if (f.type === "extension") {
      input = extensionInput(f, current);
    } else {
      input = h("input", { name: f.name, type: f.type === "number" ? "number" : f.type === "password" ? "password" : "text",
                           step: f.type === "number" ? "any" : null, value: current ?? "" });
    }
    form.append(h("label", {}, h("span", {}, f.label + (f.required ? " *" : "")), input,
                  f.help ? h("span", { class: "hint" }, f.help) : null));
  }
  if (!op.fields.length) form.append(h("p", { class: "muted" }, "No options — the CSV / selection says it all."));
}

function extensionInput(f, current) {
  const wrap = h("div", { class: "ext" });
  const profiles = PANELS.ops.value().profiles;
  if (S.extensions && S.extensions.length) {
    wrap.append(h("select", { name: f.name },
      h("option", { value: "" }, "— choose —"),
      S.extensions.map(e => {
        const id = e.id ?? e.profile_id;
        const label = e.name ?? (e.profile && e.profile.name) ?? JSON.stringify(e);
        return h("option", { value: id, selected: String(current) === String(id) }, `${label} (#${id})`);
      })));
  } else {
    wrap.append(h("input", { name: f.name, type: "number", value: current ?? "", placeholder: "extension id" }));
  }
  wrap.append(h("button", { type: "button", disabled: profiles.length !== 1,
    title: profiles.length === 1 ? "" : "Filter on exactly one profile first",
    onclick: async () => {
      try {
        const r = await api(`/api/extensions/${profiles[0]}`);
        S.extensions = r.extensions;
        if (!S.extensions.length) alert("That profile allows no extensions.");
        renderForm();
      } catch (ex) { alert(ex.message); }
    } }, "Load for selected profile"));
  return wrap;
}

function readParams() {
  const out = {};
  for (const el of $$("#op-form [name]")) {
    out[el.name] = el.type === "checkbox" ? el.checked : el.value;
  }
  return out;
}

// ---------------------------------------------------------------------------
// selection: filters and table
// ---------------------------------------------------------------------------

function setTab(tab) {
  S.tab = tab;
  $$("#tabs .tab").forEach(b => b.classList.toggle("active", b.dataset.tab === tab));
  $("#tab-filter").hidden = tab !== "filter";
  $("#tab-import").hidden = tab !== "import";
  updateSummary();
}
$$("#tabs .tab").forEach(b => b.addEventListener("click", () => setTab(b.dataset.tab)));

// The panels themselves are made once the page's elements exist; see
// PANELS below.
function filters() { return PANELS.ops.value(); }

$("#m-search").addEventListener("input", debounce(() => { S.ticked.clear(); query(); }, 300));
$$('input[name="scope"]').forEach(i => i.addEventListener("change", updateSummary));

function renderRowsMessage(text, extra = null) {
  S.querySeq++;                   // drop any page still on its way
  S.rows = [];
  S.total = 0;
  const table = $("#rows");
  $("thead", table).replaceChildren();
  $("tbody", table).replaceChildren(h("tr", {}, h("td", { class: "muted" }, text, extra ? " " : null, extra)));
  $("#pager").replaceChildren();
  $("#match-count").textContent = "";
}

// Endless scroll: query() starts over from the first page; loadMore() appends
// the next one when the table is scrolled near its end. Every response carries
// the sequence number of the query that asked for it, so a page that arrives
// after the filters changed is thrown away instead of mixed in.

const managersMode = () => S.op && S.op.target === "managers";
const rowKey = () => managersMode() ? "manager_id" : "user_id";

// ---------------------------------------------------------------------------
// user table columns
// ---------------------------------------------------------------------------

// Known fields get a label and, where it helps, a computed value. Any other
// field SAS4 returns still shows up in the picker under its own name, so a
// column added on the server needs no change here.
const NAME = r => [r.firstname, r.lastname].filter(Boolean).join(" ");
const COLUMN_DEFS = [
  ["username", "Username"],
  ["fullname", "Name", NAME],
  ["firstname", "First name"],
  ["lastname", "Last name"],
  ["profile_name", "Profile", r => r.profile_name || (r.profile_id ? `#${r.profile_id}` : "")],
  ["profile_id", "Profile ID"],
  ["parent_username", "Manager"],
  ["status", "Status", r => statusTags(r)],
  ["expiration", "Expires"],
  ["remaining_days", "Days left"],
  ["balance", "Balance"],
  ["loan_balance", "Loan balance"],
  ["debt_days", "Debt days"],
  ["traffic", "Traffic"],
  ["used_traffic", "Used traffic"],
  ["available_traffic", "Available traffic"],
  ["group_name", "Group"],
  ["site_name", "Site"],
  ["phone", "Phone"],
  ["email", "Email"],
  ["city", "City"],
  ["address", "Address"],
  ["company", "Company"],
  ["contract_id", "Contract no."],
  ["national_id", "National ID"],
  ["static_ip", "Static IP"],
  ["mikrotik_ipv6_prefix", "IPv6 prefix"],
  ["simultaneous_sessions", "Sessions"],
  ["last_online", "Last online"],
  ["online", "Online", r => (r.online ? "yes" : "")],
  ["enabled", "Enabled", r => (String(r.enabled) === "1" ? "yes" : "no")],
  ["created_at", "Created"],
  ["pin_tries", "PIN tries"],
  ["notes", "Notes"],
  ["user_id", "ID"],
];
const COMPUTED = new Set(["fullname", "status"]);
const DUP_COLUMNS = [["dup_value", "Shared"], ["dup_count", "Users sharing"],
                     ["session_mac", "Session MAC"], ["session_ip", "Session IP"],
                     ["session_state", "Session"], ["session_start", "Session start"],
                     ["nas_ip", "NAS IP"], ["session_upload", "Upload"],
                     ["session_download", "Download"], ["session_total", "Total"],
                     ["session_stop_reason", "Stop reason"]];
const NUMERIC = new Set(["balance", "loan_balance", "remaining_days", "debt_days", "traffic",
                         "used_traffic", "available_traffic", "simultaneous_sessions", "pin_tries"]);
const DEFAULT_COLUMNS = ["username", "fullname", "profile_name", "parent_username", "status",
                         "expiration", "remaining_days", "balance", "phone"];
const COLS_STORE = "sastoolkit.columns";

S.fields = [];
S.visibleCols = (() => {
  try {
    const saved = JSON.parse(localStorage.getItem(COLS_STORE));
    if (Array.isArray(saved) && saved.length) return saved;
  } catch (_) { /* private window, blocked storage */ }
  return DEFAULT_COLUMNS.slice();
})();

function labelFor(key) {
  const def = COLUMN_DEFS.find(([k]) => k === key);
  return def ? def[1] : key.replace(/_/g, " ").replace(/^./, c => c.toUpperCase());
}

// Every column the picker offers: the known ones (computed, or present in the
// data), then whatever else the data carries, in the order SAS4 sent it.
function availableColumns() {
  const present = new Set(S.fields);
  const known = COLUMN_DEFS.map(([k]) => k)
    .filter(k => COMPUTED.has(k) || present.has(k) || DEFAULT_COLUMNS.includes(k) || !S.fields.length);
  const extra = S.fields.filter(k => !COLUMN_DEFS.some(([d]) => d === k));
  return [...known, ...extra];
}

function cellValue(key, r) {
  const def = COLUMN_DEFS.find(([k]) => k === key);
  if (def && def[2]) return def[2](r);
  const v = r[key];
  if (v === null || v === undefined) return "";
  if (typeof v === "number" && Math.abs(v) >= 10000) return fmt(v);
  return v;
}

function saveColumns() {
  try { localStorage.setItem(COLS_STORE, JSON.stringify(S.visibleCols)); } catch (_) { /* ignore */ }
}

function setColumns(cols) {
  S.visibleCols = cols.length ? cols : ["username"];
  saveColumns();
  renderColumnPicker();
  if (managersMode() || !S.op || S.op.target === "new_users" || S.op.target === "server_bulk") return;
  renderTableHead();
  const body = $("#rows tbody");
  if (S.rows.length) { body.replaceChildren(); appendRows(S.rows); syncTickAll(); }
}

function renderColumnPicker() {
  const list = $("#colpick-list");
  list.replaceChildren(...availableColumns().map(key =>
    h("label", { title: key },
      h("input", { type: "checkbox", checked: S.visibleCols.includes(key), onchange: e => {
        const keep = S.visibleCols.filter(k => k !== key);
        if (e.target.checked) {
          // Keep the picker's order, so a re-ticked column returns to its place.
          const order = availableColumns();
          keep.push(key);
          keep.sort((a, b) => order.indexOf(a) - order.indexOf(b));
        }
        setColumns(keep);
      } }),
      h("span", {}, labelFor(key)),
      COMPUTED.has(key) ? null : h("span", { class: "key" }, key))));
}

$("#cols-default").addEventListener("click", () => setColumns(DEFAULT_COLUMNS.slice()));
$("#cols-all").addEventListener("click", () => setColumns(availableColumns()));
$("#cols-none").addEventListener("click", () => setColumns(["username"]));
document.addEventListener("click", e => {          // close menus on an outside click
  for (const pick of $$(".colpick")) if (pick.open && !pick.contains(e.target)) pick.open = false;
});

function tableCols() {
  if (managersMode()) {
    return [["Username", r => r.manager_username], ["Name", NAME],
            ["Balance", r => r.balance], ["Loan", r => r.loan_balance], ["Users", r => r.users_count]];
  }
  const cols = S.visibleCols.map(key => [labelFor(key), r => cellValue(key, r), key]);
  // Shown while a session filter is on, without touching the saved picks.
  const f = PANELS.ops.value();
  const keys = [...(f.duplicates.length ? DUP_COLUMNS.slice(0, 2) : []),
                ...(readsSessions(f) ? DUP_COLUMNS.slice(2) : [])];
  if (!keys.length) return cols;
  return [...keys.map(([k, t]) => [t, r => r[k] ?? "", k]),
          ...cols.filter(([, , k]) => !keys.some(([d]) => d === k))];
}

async function query() {
  if (!S.op || S.op.target === "new_users" || S.op.target === "server_bulk") return;
  if (!S.dirReady && (managersMode() || !S.partial)) return;   // managers come at the end
  S.querySeq++;
  S.rows = [];
  S.page = 0;
  S.total = 0;
  S.loading = false;
  $(".table-wrap").scrollTop = 0;
  renderTableHead();
  $("#rows tbody").replaceChildren();
  await loadMore(true);
}

async function loadMore(first = false) {
  if (S.loading || (!first && S.rows.length >= S.total)) return;
  const seq = S.querySeq, page = S.page + 1;
  S.loading = true;
  renderTableFoot();
  let r;
  try {
    r = managersMode()
      ? await api("/api/managers/query", { body: { search: $("#m-search").value, page, page_size: S.pageSize } })
      : await api("/api/users/query", { body: { filters: filters(), page, page_size: S.pageSize } });
  } catch (ex) {
    if (seq !== S.querySeq) return;
    S.loading = false;
    renderRowsMessage(ex.message, needsScan(ex) ? scanButton(() => query()) : null);
    return;
  }
  if (seq !== S.querySeq) return;          // the filters changed meanwhile
  S.loading = false;
  S.page = page;
  S.total = r.total;
  S.rows.push(...r.rows);
  if (first) {
    $("#match-count").textContent = (r.partial ? "so far " : "") + `${fmt(r.total)} matching` +
      (r.profiles && r.profiles.length > 1 && r.profiles.length <= 6
        ? " · " + r.profiles.map(([n, c]) => `${n}: ${fmt(c)}`).join(", ") : "");
    updateSummary();
  }
  appendRows(r.rows);
  syncTickAll();
  renderTableFoot();
  fillViewport();
}

// Keep loading while the rows do not yet fill the box -- otherwise there is
// no scrollbar to scroll, and nothing would ever ask for page two.
function fillViewport() {
  const wrap = $(".table-wrap");
  if (wrap.scrollHeight <= wrap.clientHeight + 40 && S.rows.length < S.total) loadMore();
}

$(".table-wrap").addEventListener("scroll", e => {
  const wrap = e.currentTarget;
  if (wrap.scrollTop + wrap.clientHeight >= wrap.scrollHeight - 300) loadMore();
});

function renderTableHead() {
  const cols = tableCols();
  const all = h("input", { type: "checkbox", id: "tick-all", title: "Tick all loaded rows",
    onchange: e => {
      const key = rowKey();
      for (const r of S.rows) e.target.checked ? S.ticked.add(r[key]) : S.ticked.delete(r[key]);
      $$("#rows tbody input[data-key]").forEach(i => { i.checked = e.target.checked; });
      updateSummary();
    } });
  $("#rows thead").replaceChildren(h("tr", {}, h("th", {}, all), cols.map(([t]) => h("th", {}, t))));
}

function appendRows(rows) {
  const body = $("#rows tbody"), cols = tableCols(), key = rowKey();
  const frag = document.createDocumentFragment();
  for (const r of rows) {
    frag.append(h("tr", {},
      h("td", {}, h("input", { type: "checkbox", "data-key": r[key], checked: S.ticked.has(r[key]),
        onchange: e => {
          e.target.checked ? S.ticked.add(r[key]) : S.ticked.delete(r[key]);
          syncTickAll();
          updateSummary();
        } })),
      cols.map(([, get, k]) => h("td", { class: NUMERIC.has(k) ? "num" : null }, get(r)))));
  }
  body.append(frag);
}

function syncTickAll() {
  const all = $("#tick-all"), key = rowKey();
  if (all) all.checked = S.rows.length > 0 && S.rows.every(r => S.ticked.has(r[key]));
}

function renderTableFoot() {
  const foot = $("#pager");
  if (S.loading) { foot.replaceChildren(h("span", { class: "muted" }, "Loading…")); return; }
  if (!S.total) {
    foot.replaceChildren();
    $("#rows tbody").replaceChildren(h("tr", {}, h("td", { colspan: tableCols().length + 1, class: "muted" }, "Nothing matches.")));
    return;
  }
  foot.replaceChildren(h("span", { class: "muted" },
    S.rows.length < S.total
      ? `Showing ${fmt(S.rows.length)} of ${fmt(S.total)} — scroll for more`
      : `All ${fmt(S.total)} shown`));
}

function statusTags(r) {
  const tags = [];
  const exp = r.expiration ? new Date(String(r.expiration).replace(" ", "T")) : null;
  if (String(r.enabled) !== "1") tags.push(h("span", { class: "tag off" }, "disabled"));
  else if (exp && exp <= new Date()) tags.push(h("span", { class: "tag off" }, "expired"));
  else tags.push(h("span", { class: "tag on" }, "active"));
  if (r.online) tags.push(" ", h("span", { class: "tag on" }, "online"));
  return h("span", {}, tags);
}

// ---------------------------------------------------------------------------
// export
// ---------------------------------------------------------------------------

function exportScope() {
  const ticked = $('input[name="scope"]:checked').value === "ticked";
  return { ticked, count: ticked ? S.ticked.size : S.total };
}

function updateExportScope() {
  const el = $("#export-scope");
  if (!el) return;
  const { ticked, count } = exportScope();
  const ready = S.dirReady;
  el.textContent = !ready ? "Available once the user list has finished loading."
    : !count ? (ticked ? "Tick some users first." : "Nothing matches the filter.")
    : `Exports ${fmt(count)} user(s) — ${ticked ? "the ticked rows" : "everything matching the filter"}.`;
  $$("#exportpick [data-format]").forEach(b => { b.disabled = !ready || !count; });
}

$$("#exportpick [data-format]").forEach(btn => btn.addEventListener("click", async () => {
  const err = $("#export-error");
  err.hidden = true;
  const { ticked } = exportScope();
  const allFields = $('input[name="export-cols"]:checked').value === "all";
  const body = {
    filters: filters(), format: btn.dataset.format,
    ids: ticked ? [...S.ticked] : null,
    columns: allFields ? [] : S.visibleCols.map(k => ({ key: k, label: labelFor(k) })),
  };
  const label = btn.textContent;
  btn.disabled = true;
  btn.textContent = "Preparing…";
  try {
    await download("/api/users/export", body, `sas4_users.${body.format}`);
    $("#exportpick").open = false;
  } catch (ex) {
    err.textContent = ex.message;
    err.hidden = false;
  } finally {
    btn.textContent = label;
    updateExportScope();
  }
}));

// ---------------------------------------------------------------------------
// CSV import
// ---------------------------------------------------------------------------

$("#import-form").addEventListener("submit", async e => {
  e.preventDefault();
  const file = $("#import-file").files[0];
  if (!file) return;
  const out = $("#import-result");
  out.replaceChildren(h("p", { class: "muted" }, "Uploading…"));
  const form = new FormData();
  form.append("op", S.op.key);
  form.append("file", file);
  try {
    const r = await api("/api/import", { form });
    S.imported = { import_id: r.import_id, matched: r.matched, target: S.op.target };
    out.replaceChildren(
      h("p", {}, h("b", {}, `${fmt(r.matched)} row(s) ready`),
        r.unmatched_count ? h("span", { class: "error" }, ` · ${fmt(r.unmatched_count)} not usable`) : null,
        h("span", { class: "muted" }, ` · columns: ${r.columns.join(", ")}`)),
      r.unmatched_count ? h("details", { open: true },
        h("summary", {}, "Rows that will not run"),
        rowsTable(r.unmatched, ["line", "username", "manager_username", "reason"])) : "",
      r.preview.length ? h("details", {},
        h("summary", {}, "Preview"),
        rowsTable(r.preview, Object.keys(r.preview[0]).filter(k => k !== "line").slice(0, 10))) : "");
  } catch (ex) {
    S.imported = null;
    out.replaceChildren(h("p", { class: "error" }, ex.message));
  }
  updateSummary();
});

function rowsTable(rows, cols) {
  cols = cols.filter(c => rows.some(r => r[c] !== undefined && r[c] !== null && r[c] !== ""));
  return h("div", { class: "table-wrap" }, h("table", {},
    h("thead", {}, h("tr", {}, cols.map(c => h("th", {}, c)))),
    h("tbody", {}, rows.map(r => h("tr", {}, cols.map(c => h("td", { class: c === "message" ? "msg" : null }, r[c] ?? "")))))));
}

// The "shared with" filters on session MAC / IP need the last-session scan.
const needsScan = ex => /scan the sessions first/.test(ex.message);

function scanButton(then) {
  const btn = h("button", { type: "button", onclick: async () => {
    btn.disabled = true;
    const days = Number($("#ls-lookback").value);
    btn.textContent = "Scanning sessions…";
    try {
      await api("/api/sessions/refresh", { body: { lookback_days: days } });
      for (;;) {
        const status = await api("/api/sessions/status");
        if (status.state === "error") throw new Error("Reading sessions failed: " + status.error);
        if (status.state === "ready") break;
        const p = status.progress || {};
        btn.textContent = `Scanning sessions… ${fmt(p.seen || 0)} / ${fmt(p.users || 0)} users`;
        await new Promise(r => setTimeout(r, 1000));
      }
      S.ls.data = null;               // the Last session report reads it afresh
      refreshPanels();                // stop reasons from this scan
      then();
    } catch (ex) {
      btn.disabled = false;
      btn.textContent = "Scan sessions";
      btn.after(h("span", { class: "error" }, " " + ex.message));
    }
  } }, "Scan sessions");
  return btn;
}

// ---------------------------------------------------------------------------
// run
// ---------------------------------------------------------------------------

function selection() {
  if (!S.op) return null;
  if (S.op.target === "server_bulk") {
    const p = S.bulkPreview;
    if (!p || p.sig !== bulkSig()) return { count: 0, why: "Preview the matching users first." };
    if (p.problems && p.problems.length) return { count: 0, why: "SAS4 returned users that do not match the filter — fix it before applying." };
    if (p.mode === "each") return { count: p.total, each: true, text: `${fmt(p.total)} user(s) matching the filter, one request each` };
    return { count: p.total, text: `${fmt(p.total)} user(s) matching the filter` };
  }
  if (S.tab === "import" || S.op.target === "new_users") {
    if (!S.imported || S.imported.target !== S.op.target) return { count: 0, why: "Upload a CSV first." };
    return { count: S.imported.matched, source: { type: "import", import_id: S.imported.import_id },
             text: `${fmt(S.imported.matched)} row(s) from the CSV` };
  }
  if (!S.dirReady) return { count: 0, why: "Wait for the user list to finish loading." };
  const managers = S.op.target === "managers";
  const ticked = $('input[name="scope"]:checked').value === "ticked";
  const source = managers ? { type: "managers", search: $("#m-search").value } : { type: "filter", filters: filters() };
  if (ticked) source.ids = [...S.ticked];
  const count = ticked ? S.ticked.size : S.total;
  return { count, source, text: `${fmt(count)} ${managers ? "manager" : "user"}(s) — ${ticked ? "ticked rows" : "everything matching the filter"}` };
}

function updateSummary() {
  $("#ticked-count").textContent = fmt(S.ticked.size);
  updateExportScope();
  const sel = selection(), el = $("#selection-summary");
  const dry = $("#dry-run").checked;
  if (!sel) return;
  el.textContent = sel.count ? `Runs on ${sel.text}.` : (sel.why || "Nothing selected.");
  const run = $("#run");
  run.disabled = !sel.count;
  if (S.op.target === "server_bulk" && !sel.each) {
    run.className = "big danger";
    run.textContent = `Apply to ${fmt(sel.count)} user(s)`;
    return;
  }
  run.className = "big " + (dry ? "primary" : "danger");
  run.textContent = dry ? `Dry run on ${fmt(sel.count)}` : `Run LIVE on ${fmt(sel.count)}`;
}
$("#dry-run").addEventListener("change", updateSummary);

$("#run").addEventListener("click", async () => {
  const err = $("#run-error");
  err.hidden = true;
  const sel = selection();
  if (!sel || !sel.count) return;
  if (S.op.target === "server_bulk") return applyBulk(sel);
  const params = readParams(), dry = $("#dry-run").checked;
  const ok = await confirmRun(S.op, params, sel, dry);
  if (!ok) return;
  try {
    const job = await api("/api/jobs", { body: { op: S.op.key, params, dry_run: dry,
      workers: Number($("#workers").value) || 4, source: sel.source } });
    S.openJobs.add(job.id);
    refreshJobs();
    $(".jobs").scrollIntoView({ behavior: "smooth" });
  } catch (ex) {
    err.textContent = ex.message; err.hidden = false;
  }
});

function describeParams(op, params) {
  return op.fields.map(f => {
    let v = params[f.name];
    if (f.type === "checkbox") v = v ? "yes" : "no";
    else if (f.type === "select") v = (f.options.find(([k]) => String(k) === String(v)) || [v, v])[1];
    else if (f.type === "profile" || f.type === "manager" || f.type === "site") {
      const items = f.type === "profile" ? S.meta.profiles : f.type === "manager" ? S.meta.managers : S.meta.sites;
      const it = items.find(i => String(i.id) === String(v));
      v = it ? it.name : v;
    } else if (f.type === "password" && v) v = "***";
    return v === "" || v === undefined ? null : h("li", {}, `${f.label}: `, h("b", {}, v));
  }).filter(Boolean);
}

function confirmRun(op, params, sel, dry) {
  const dlg = $("#confirm");
  $("#confirm-title").textContent = `${op.label} — ${dry ? "dry run" : "LIVE"}`;
  $("#confirm-body").replaceChildren(
    h("p", {}, dry ? "Nothing will be sent to SAS4. " : h("b", {}, "This changes SAS4 for real. "),
      `Rows: ${sel.text}.`),
    h("ul", {}, describeParams(op, params)));
  const mustType = !dry && (op.danger || sel.count > 1);
  const expect = String(sel.count);
  $("#confirm-type-wrap").hidden = !mustType;
  $("#confirm-expect").textContent = expect;
  const typed = $("#confirm-type"), okBtn = $("#confirm-ok");
  typed.value = "";
  okBtn.className = dry ? "primary" : "danger";
  okBtn.disabled = mustType;
  typed.oninput = () => { okBtn.disabled = typed.value.trim() !== expect; };
  dlg.showModal();
  if (mustType) typed.focus();
  return new Promise(resolve => {
    dlg.onclose = () => resolve(dlg.returnValue === "ok");
  });
}

// ---------------------------------------------------------------------------
// server-side bulk change
// ---------------------------------------------------------------------------

S.bulkRows = [{ key: "profile_id", op: "=", value: "" }];
S.bulkPreview = null;

// The extra filters, or null when none is set (one SAS4 request then).
function bulkExtra() { return PANELS.bulk.active() ? PANELS.bulk.value() : null; }

function bulkSig() { return JSON.stringify([S.bulkRows, bulkExtra()]); }

// With an extra filter the conditions are optional, and the action is sent
// once per user -- so the dry run / parallel options apply.
function bulkFiltersChanged() {
  if (!S.meta) return;
  const each = PANELS.bulk.active();
  if (!each && !S.bulkRows.length) S.bulkRows.push({ key: "profile_id", op: "=", value: "" });
  $("#run-opts").hidden = !each;
  S.bulkPreview = null;
  $("#bulk-result").replaceChildren();
  renderBulkRows();
  updateSummary();
}

function idChoices(type) {
  return type === "profile" ? S.meta.profiles : type === "manager" ? S.meta.managers
       : type === "site" ? S.meta.sites : null;
}

function valueInput(type, current, onchange, placeholder) {
  const items = idChoices(type);
  if (items) {
    return h("select", { onchange },
      h("option", { value: "" }, "— choose —"),
      items.map(i => h("option", { value: i.id, selected: String(current) === String(i.id) }, `${i.name} (#${i.id})`)));
  }
  if (type === "bool") {
    return h("select", { onchange },
      h("option", { value: "" }, "— choose —"),
      h("option", { value: "1", selected: String(current) === "1" }, "Yes"),
      h("option", { value: "0", selected: String(current) === "0" }, "No"));
  }
  return h("input", { type: type === "number" || type === "group" ? "number" : type === "date" ? "date" : "text",
                      value: current ?? "", placeholder: placeholder || "", oninput: onchange });
}

function renderBulkRows() {
  const box = $("#bulk-rows"), meta = S.meta.bulk;
  box.replaceChildren();
  S.bulkRows.forEach((row, i) => {
    const key = meta.keys.find(k => k.key === row.key) || meta.keys[0];
    const pickOnly = ["profile", "manager", "site", "bool"].includes(key.type);
    const ops = pickOnly ? ["=", "!="] : meta.ops;
    if (!ops.includes(row.op)) row.op = "=";
    const changed = () => { S.bulkPreview = null; updateSummary(); };
    box.append(h("div", { class: "bulk-row" },
      h("select", { onchange: e => { row.key = e.target.value; row.value = ""; changed(); renderBulkRows(); } },
        meta.keys.map(k => h("option", { value: k.key, selected: k.key === row.key }, k.label))),
      h("select", { onchange: e => { row.op = e.target.value; changed(); } },
        ops.map(o => h("option", { value: o, selected: o === row.op }, o))),
      valueInput(key.type, row.value, e => { row.value = e.target.value; changed(); },
                 row.op === "like" ? "use % as wildcard" : ""),
      h("button", { type: "button", title: "Remove", disabled: S.bulkRows.length === 1 && !PANELS.bulk.active(),
        onclick: () => { S.bulkRows.splice(i, 1); changed(); renderBulkRows(); } }, "×")));
  });
}

$("#bulk-add").addEventListener("click", () => {
  S.bulkRows.push({ key: "profile_id", op: "=", value: "" });
  S.bulkPreview = null;
  renderBulkRows();
  updateSummary();
});

$("#bulk-preview").addEventListener("click", previewBulk);

async function previewBulk() {
  const out = $("#bulk-result"), sig = bulkSig(), extra = bulkExtra();
  out.replaceChildren(h("p", { class: "muted" }, "Counting…"));
  try {
    const r = await api("/api/bulk/preview", { body: { filters: S.bulkRows, extra } });
    S.bulkPreview = { ...r, sig };
    if (r.mode === "each") {
      out.replaceChildren(
        h("p", {}, h("span", { class: "big-count" }, fmt(r.total)), " user(s) match",
          r.rows.some(x => x.dup_value) ? ", grouped by what they share." : "."),
        r.rows.length < r.total ? h("p", { class: "muted" }, `First ${fmt(r.rows.length)} shown.`) : "",
        r.rows.length ? bulkEachTable(r.rows) : "");
      updateSummary();
      return;
    }
    out.replaceChildren(
      h("p", {}, h("span", { class: "big-count" }, fmt(r.total)), " user(s) match."),
      r.problems.length ? h("p", { class: "error" },
        "SAS4 returned users that do not match this filter, so it may be ignoring it. Applying is blocked.\n" +
        r.problems.slice(0, 5).join("\n")) : "",
      r.sample.length ? h("details", { open: true },
        h("summary", {}, `First ${r.sample.length}`),
        rowsTable(r.sample, ["username", "firstname", "lastname", "profile_name", "parent_username", "enabled", "expiration"])) : "");
  } catch (ex) {
    S.bulkPreview = null;
    out.replaceChildren(h("p", { class: "error" }, ex.message, needsScan(ex) ? " " : null,
      needsScan(ex) ? scanButton(previewBulk) : null));
  }
  updateSummary();
}

function bulkEachTable(rows) {
  const cols = [["Shared", r => r.dup_value], ["Users", r => r.dup_count], ["Username", r => r.username],
                ["Name", NAME], ["Profile", r => r.profile_name], ["Manager", r => r.parent_username],
                ["Session MAC", r => r.session_mac], ["Session IP", r => r.session_ip],
                ["Session", r => r.session_state], ["Session start", r => r.session_start],
                ["NAS IP", r => r.nas_ip], ["Upload", r => r.session_upload],
                ["Download", r => r.session_download], ["Total", r => r.session_total],
                ["Stop reason", r => r.session_stop_reason], ["Static IP", r => r.static_ip]]
    .filter(([, get]) => rows.some(r => get(r) !== undefined && get(r) !== null && get(r) !== ""));
  return h("div", { class: "table-wrap" }, h("table", {},
    h("thead", {}, h("tr", {}, cols.map(([t]) => h("th", {}, t)))),
    h("tbody", {}, rows.map(r => h("tr", {}, cols.map(([, get]) => h("td", {}, get(r) ?? "")))))));
}

function renderBulkAction(form, old) {
  const actions = S.meta.bulk.actions;
  const name = old.action && actions.some(a => a.name === old.action) ? old.action : actions[0].name;
  const action = actions.find(a => a.name === name);
  const select = h("select", { name: "action", onchange: () => renderForm() },
    actions.map(a => h("option", { value: a.name, selected: a.name === name }, a.label + (a.danger ? "  ⚠" : ""))));
  form.append(h("label", {}, h("span", {}, "Action *"), select));
  if (action.type) {
    const input = valueInput(action.type === "text" && name === "set_expiration" ? "text" : action.type,
                             old.action === name ? old.option : "", () => {},
                             name === "set_expiration" ? "YYYY-MM-DD HH:MM:SS" : "");
    input.name = "option";
    const unit = /\(MB\)/.test(action.label) ? "MB — 1 GB = 1024" : "";
    form.append(h("label", {}, h("span", {}, "Value *"), input, unit ? h("span", { class: "hint" }, unit) : null));
  }
  if (name === "set_traffic_balance")
    form.append(h("p", { class: "warn" }, "Sets each user's balance to this value — it replaces what they have. Use Add Traffic to add on top."));
  if (name === "delete")
    form.append(h("p", { class: "warn" }, "Deletes every matching user. This cannot be undone."));
}

async function applyBulk(sel) {
  const err = $("#run-error");
  const params = readParams();
  const action = S.meta.bulk.actions.find(a => a.name === params.action);
  if (action.type && (params.option === undefined || params.option === "")) {
    err.textContent = "Fill in the value."; err.hidden = false; return;
  }
  const dlg = $("#confirm");
  $("#confirm-title").textContent = `Bulk change — ${action.label}`;
  const conds = S.bulkRows.map(r => {
    const key = S.meta.bulk.keys.find(k => k.key === r.key);
    const items = idChoices(key.type);
    const shown = items ? (items.find(i => String(i.id) === String(r.value)) || {}).name || r.value
                : key.type === "bool" ? (r.value === "1" ? "Yes" : "No") : r.value;
    return h("li", {}, `${key.label} ${r.op} `, h("b", {}, shown));
  });
  let value = params.option;
  const items = idChoices(action.type);
  if (items) value = (items.find(i => String(i.id) === String(value)) || {}).name || value;
  const extra = bulkExtra(), dry = sel.each && $("#dry-run").checked;
  if (extra) {
    for (const d of PANELS.bulk.describe()) conds.push(h("li", {}, h("b", {}, d)));
    $("#confirm-title").textContent += dry ? " — dry run" : " — LIVE";
  }
  $("#confirm-body").replaceChildren(
    sel.each
      ? h("p", {}, dry ? "Nothing will be sent to SAS4. " : h("b", {}, "This changes SAS4 for real, one request per user. "),
          `Matched now: ${sel.text}.`)
      : h("p", {}, h("b", {}, "SAS4 will apply this to every matching user, in one request. "),
          `Matched now: ${sel.text}.`),
    h("p", {}, "Action: ", h("b", {}, action.label + (action.type ? ` = ${value}` : ""))),
    h("p", {}, "Where:"), h("ul", {}, conds),
    h("p", { class: "muted" }, sel.each
      ? "The match is worked out again right before starting; if the count changed, nothing is sent. Each user's username filter is checked to find only that user."
      : "The count is checked again right before sending; if it changed, nothing is sent."));
  const expect = String(sel.count), typed = $("#confirm-type"), okBtn = $("#confirm-ok");
  $("#confirm-type-wrap").hidden = dry;
  $("#confirm-expect").textContent = expect;
  typed.value = "";
  okBtn.className = dry ? "primary" : "danger";
  okBtn.disabled = !dry;
  typed.oninput = () => { okBtn.disabled = typed.value.trim() !== expect; };
  dlg.showModal();
  if (!dry) typed.focus();
  const ok = await new Promise(resolve => { dlg.onclose = () => resolve(dlg.returnValue === "ok"); });
  if (!ok) return;
  try {
    const job = await api("/api/bulk/apply", { body: { filters: S.bulkRows, action: params.action,
      option: params.option, expected_count: sel.count, extra,
      dry_run: dry, workers: Number($("#workers").value) || 4 } });
    S.openJobs.add(job.id);
    S.bulkPreview = null;          // a fresh preview is needed for another run
    updateSummary();
    refreshJobs();
    $(".jobs").scrollIntoView({ behavior: "smooth" });
  } catch (ex) {
    err.textContent = ex.message; err.hidden = false;
  }
}

// ---------------------------------------------------------------------------
// jobs
// ---------------------------------------------------------------------------

async function refreshJobs() {
  clearTimeout(S.jobsTimer);
  let r;
  try { r = await api("/api/jobs"); } catch (_) { return; }
  const list = $("#jobs");
  if (!r.jobs.length) { list.replaceChildren(h("p", { class: "muted" }, "No jobs yet.")); return; }
  const details = {};
  for (const id of S.openJobs) {
    const job = r.jobs.find(j => j.id === id);
    if (job) details[id] = await api(`/api/jobs/${id}`).catch(() => null);
  }
  list.replaceChildren(...r.jobs.map(j => jobCard(j, details[j.id])));
  if (r.jobs.some(j => j.state === "running" || j.state === "queued"))
    S.jobsTimer = setTimeout(refreshJobs, 1500);
}

function jobCard(j, detail) {
  const pct = j.total ? Math.round(100 * j.done / j.total) : 0;
  const running = j.state === "running" || j.state === "queued";
  const failed = j.counts.failed || 0;
  const card = h("div", { class: "job" },
    h("div", { class: "job-head" },
      h("span", { class: "title" }, `#${j.id} ${j.label}`),
      h("span", { class: "tag " + (j.dry_run ? "" : "off") }, j.dry_run ? "dry run" : "live"),
      j.retry_of ? h("span", { class: "muted" }, `retry of #${j.retry_of}`) : null,
      h("span", { class: "muted" }, `${j.state} · ${fmt(j.done)}/${fmt(j.total)}`),
      h("div", { class: "spacer" }),
      running ? h("button", { onclick: async () => { await api(`/api/jobs/${j.id}/cancel`, { method: "POST" }); refreshJobs(); } }, "Cancel") : null,
      !running && !j.dry_run && failed && j.op !== "bulk_change" ? h("button", { onclick: async () => {
        if (!confirm(`Send the ${failed} failed row(s) again?`)) return;
        try { const nj = await api(`/api/jobs/${j.id}/retry-failed`, { method: "POST" }); S.openJobs.add(nj.id); refreshJobs(); }
        catch (ex) { alert(ex.message); }
      } }, `Retry ${failed} failed`) : null,
      h("button", { onclick: async () => {
        try { await download(`/api/jobs/${j.id}/results.csv`, undefined, `job_${j.id}.csv`); }
        catch (ex) { alert(ex.message); }
      } }, "Results CSV")),
    j.description ? h("div", { class: "muted" }, j.description) : null,
    h("div", { class: "bar" }, h("div", { style: `width:${pct}%` })),
    h("div", { class: "counts" },
      Object.entries(j.counts).map(([k, v]) => h("span", { class: "c-" + k }, `${k}: ${fmt(v)}`)),
      h("span", { class: "muted" }, `saved to ${j.report}`)));
  const det = h("details", { open: S.openJobs.has(j.id) },
    h("summary", {}, "Latest rows"),
    detail && detail.recent.length
      ? rowsTable(detail.recent.slice().reverse(), ["username", "manager_username", "action", "http_status", "message", "payload"])
      : h("p", { class: "muted" }, "Nothing yet."));
  det.addEventListener("toggle", () => {
    const wasOpen = S.openJobs.has(j.id);
    det.open ? S.openJobs.add(j.id) : S.openJobs.delete(j.id);
    if (det.open && !wasOpen) refreshJobs();
  });
  card.append(det);
  return card;
}

// ---------------------------------------------------------------------------
// views
// ---------------------------------------------------------------------------

function showView(name) {
  S.view = name;
  $$("#views .view").forEach(b => b.classList.toggle("active", b.dataset.view === name));
  $("#app-view").hidden = name !== "ops";
  $("#reports-view").hidden = name !== "reports";
  if (name === "reports") showReport(S.reportTab || "activity");
}
$$("#views .view").forEach(b => b.addEventListener("click", () => showView(b.dataset.view)));

// ---------------------------------------------------------------------------
// reports
// ---------------------------------------------------------------------------

// Local calendar date (toISOString is UTC, a day off around midnight in Libya).
const iso = d => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
const daysAgo = n => { const d = new Date(); d.setDate(d.getDate() - n); return iso(d); };
const DEFAULT_RANGE = { day: 29, week: 7 * 12 - 1, month: 365 };

function setDefaultRange() {
  $("#r-from").value = daysAgo(DEFAULT_RANGE[$("#r-period").value]);
  $("#r-to").value = daysAgo(0);
}
$("#r-period").addEventListener("change", setDefaultRange);
setDefaultRange();

// Two reports share the view: "Users over time" and "Last session".
function showReport(name) {
  S.reportTab = name;
  $$("#report-tabs .tab").forEach(b => b.classList.toggle("active", b.dataset.report === name));
  $("#rep-activity").hidden = name !== "activity";
  $("#rep-sessions").hidden = name !== "sessions";
  if (name === "activity") reportReady(); else sessionsReady();
}
$$("#report-tabs .tab").forEach(b => b.addEventListener("click", () => showReport(b.dataset.report)));

function reportReady() {
  $("#r-wait").hidden = S.dirReady;
  $("#r-run").disabled = !S.dirReady;
  if (S.dirReady && !S.report) runReport();
}

function reportBody(format) {
  return { period: $("#r-period").value, date_from: $("#r-from").value || null,
           date_to: $("#r-to").value || null,
           filters: PANELS.activity.value(),
           format: format || null };
}

async function runReport() {
  const err = $("#r-error");
  err.hidden = true;
  $("#r-run").disabled = true;
  try {
    S.report = await api("/api/reports", { body: reportBody() });
    renderReport();
  } catch (ex) {
    err.replaceChildren(ex.message, needsScan(ex) ? " " : "", needsScan(ex) ? scanButton(runReport) : "");
    err.hidden = false;
  } finally {
    $("#r-run").disabled = !S.dirReady;
  }
}
$("#r-run").addEventListener("click", runReport);

// POST a body (or GET when there is none), save the file the server answers
// with. The desktop window cannot download a blob, so there the server saves
// it through a native Save dialog and answers {saved} or {cancelled}.
async function download(path, body, fallbackName) {
  const headers = {};
  if (S.session && S.session.desktop) headers["X-Save-Dialog"] = "1";
  const init = { method: body === undefined ? "GET" : "POST", credentials: "same-origin", headers };
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  const resp = await fetch(path, init);
  if (!resp.ok) throw new Error((await resp.json().catch(() => ({}))).detail || `HTTP ${resp.status}`);
  if (headers["X-Save-Dialog"]) {
    const result = await resp.json();
    if (result.saved) alert(`Saved to ${result.saved}`);
    return;
  }
  const blob = await resp.blob();
  const m = /filename="([^"]+)"/.exec(resp.headers.get("Content-Disposition") || "");
  const a = h("a", { href: URL.createObjectURL(blob), download: m ? m[1] : fallbackName });
  document.body.append(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 10000);
}

for (const [id, format] of [["#r-xlsx", "xlsx"], ["#r-csv", "csv"]]) {
  $(id).addEventListener("click", async () => {
    const err = $("#r-error");
    err.hidden = true;
    try { await download("/api/reports", reportBody(format), `report.${format}`); }
    catch (ex) { err.textContent = ex.message; err.hidden = false; }
  });
}

function renderReport() {
  const r = S.report, t = r.totals;
  $("#r-body").hidden = false;
  $("#r-xlsx").disabled = $("#r-csv").disabled = false;
  const range = `${r.from} → ${r.to}`;
  const tile = (label, value, sub) => h("div", { class: "kpi" },
    h("div", { class: "k-label" }, label), h("div", { class: "k-value" }, fmt(value)),
    sub ? h("div", { class: "k-sub" }, sub) : null);
  $("#r-kpis").replaceChildren(
    tile("Active now", t.active_now, `of ${fmt(t.users)} users in this report`),
    tile("New", t.new, range),
    tile("Expired", t.expired, range),
    t.expiring ? tile("Expiring", t.expiring, "still ahead, within the range") : null);

  const labels = r.rows.map(x => x.period);
  lineChart($("#r-active-chart"), labels, r.rows.map(x => x.active), "Active");
  $("#r-flow-legend").replaceChildren(
    h("span", {}, h("span", { class: "swatch sw-1" }), "New"),
    h("span", {}, h("span", { class: "swatch sw-2" }), "Expired / expiring"));
  barChart($("#r-flow-chart"), labels, [
    { name: "New", cls: "s1", sw: "sw-1", values: r.rows.map(x => x.new) },
    { name: "Expired / expiring", cls: "s2", sw: "sw-2", values: r.rows.map(x => x.expired + x.expiring),
      note: r.rows.map(x => x.expiring ? `${fmt(x.expiring)} still ahead` : "") }]);

  const cols = [["period", "Period"], ["start", "From"], ["end", "To"], ["active", "Active at end", 1],
                ["new", "New", 1], ["expired", "Expired", 1], ["expiring", "Expiring", 1]];
  $("#r-table thead").replaceChildren(h("tr", {}, cols.map(([, l, n]) => h("th", { class: n ? "num" : null }, l))));
  $("#r-table tbody").replaceChildren(...r.rows.map(row => h("tr", {},
    cols.map(([k, , n]) => h("td", { class: n ? "num" : null },
      row[k] === null ? "—" : n ? fmt(row[k]) : row[k])))));
  const notes = [`Generated ${r.generated_at}.`];
  if (t.no_created) notes.push(`${fmt(t.no_created)} user(s) have no creation date: they are never counted as new, and count as active from the start.`);
  if (t.no_expiration) notes.push(`${fmt(t.no_expiration)} user(s) have no expiry date and are left out of active and expired.`);
  notes.push("“—” = a future period, which has no active figure.");
  $("#r-foot").textContent = notes.join(" ");
}

window.addEventListener("resize", debounce(() => { if (S.report && S.view === "reports") renderReport(); }, 200));

// ---- last session ---------------------------------------------------------

S.ls = { page: 1, pageSize: 100, data: null, polling: false, seq: 0 };

const LS_COLS = [
  ["username", "Username"], ["fullname", "Name"], ["profile_name", "Profile"],
  ["parent_username", "Manager"], ["state", "Status"], ["start", "Session start"],
  ["stop", "Session stop"], ["duration_h", "Duration"], ["ip", "IP address"],
  ["mac", "MAC"], ["nas_ip", "NAS IP"], ["called_station", "Called station"],
  ["nas_port", "NAS port"], ["upload_h", "Upload", 1], ["download_h", "Download", 1],
  ["total_h", "Total", 1], ["terminate_cause", "Stop reason"], ["expiration", "Expiration"]];

const STATE_TAG = { online: ["Online", "on"], offline: ["Offline", null], none: ["No session", "off"] };

function sessionsBody(format) {
  return { filters: PANELS.sessions.value(), page: S.ls.page, page_size: S.ls.pageSize,
           format: format || null };
}

// Called whenever the tab is shown: picks up a scan already done (or running)
// for this login rather than starting another.
async function sessionsReady() {
  $("#ls-wait").hidden = S.dirReady;
  $("#ls-scan").disabled = !S.dirReady;
  if (!S.dirReady) return;
  let status;
  try { status = await api("/api/sessions/status"); } catch (_) { return; }
  if (status.state === "loading") pollSessions();
  else if (status.state === "ready") { if (!S.ls.data) querySessions(); }
  else if (status.state === "error") showSessionsError(status.error);
}

function showSessionsError(message) {
  const err = $("#ls-error");
  err.textContent = message; err.hidden = !message;
}

$("#ls-scan").addEventListener("click", async () => {
  showSessionsError("");
  $("#ls-scan").disabled = true;
  try {
    await api("/api/sessions/refresh", { body: { lookback_days: Number($("#ls-lookback").value) } });
    pollSessions();
  } catch (ex) {
    showSessionsError(ex.message);
    $("#ls-scan").disabled = !S.dirReady;
  }
});

async function pollSessions() {
  if (S.ls.polling) return;
  S.ls.polling = true;
  $("#ls-scan").disabled = true;
  $("#ls-progress").hidden = false;
  try {
    for (;;) {
      const status = await api("/api/sessions/status");
      const p = status.progress || {};
      $("#ls-progress-text").textContent =
        `${fmt(p.pages || 0)} page(s) · ${fmt(p.sessions || 0)} sessions read · ` +
        `${fmt(p.seen || 0)} / ${fmt(p.users || 0)} users found`;
      if (status.state !== "loading") {
        if (status.state === "error") showSessionsError("Reading sessions failed: " + status.error);
        else { S.ls.page = 1; refreshPanels(); await querySessions(); }
        break;
      }
      await new Promise(r => setTimeout(r, 1000));
    }
  } catch (ex) {
    showSessionsError(ex.message);
  } finally {
    S.ls.polling = false;
    $("#ls-progress").hidden = true;
    $("#ls-scan").disabled = !S.dirReady;
  }
}

async function querySessions() {
  const seq = ++S.ls.seq;
  try {
    const data = await api("/api/sessions/query", { body: sessionsBody() });
    if (seq !== S.ls.seq) return;               // a newer filter won
    S.ls.data = data;
    showSessionsError("");
    renderSessions();
  } catch (ex) {
    if (seq === S.ls.seq) showSessionsError(ex.message);
  }
}

const requerySessions = () => { if (S.ls.data) { S.ls.page = 1; querySessions(); } };
$("#ls-prev").addEventListener("click", () => { S.ls.page--; querySessions(); });
$("#ls-next").addEventListener("click", () => { S.ls.page++; querySessions(); });

for (const [id, format] of [["#ls-xlsx", "xlsx"], ["#ls-csv", "csv"]]) {
  $(id).addEventListener("click", async () => {
    showSessionsError("");
    $(id).disabled = true;
    try { await download("/api/sessions/query", sessionsBody(format), `last_sessions.${format}`); }
    catch (ex) { showSessionsError(ex.message); }
    finally { $(id).disabled = false; }
  });
}

function stateTag(state) {
  const [label, cls] = STATE_TAG[state] || [state, null];
  return h("span", { class: cls ? `tag ${cls}` : "tag" }, label);
}

function renderSessions() {
  const d = S.ls.data, t = d.totals;
  $("#ls-body").hidden = false;
  $("#ls-xlsx").disabled = $("#ls-csv").disabled = !d.total;
  const span = d.lookback_days ? `in the last ${d.lookback_days} days` : "in the whole history";
  const tile = (label, value, sub) => h("div", { class: "kpi" },
    h("div", { class: "k-label" }, label), h("div", { class: "k-value" }, fmt(value)),
    sub ? h("div", { class: "k-sub" }, sub) : null);
  $("#ls-kpis").replaceChildren(
    tile("Users", t.users, "matching the filters"),
    tile("Online now", t.online, "last session still open"),
    tile("Offline", t.offline, `last session ended, ${span}`),
    tile("No session", t.none, span));
  const p = d.progress || {};
  $("#ls-scan-info").textContent = `Scanned ${d.scanned_at} · ${fmt(p.pages)} SAS4 request(s) · stopped: ${d.stopped}`;

  $("#ls-table thead").replaceChildren(h("tr", {}, LS_COLS.map(([, l, n]) => h("th", { class: n ? "num" : null }, l))));
  $("#ls-table tbody").replaceChildren(...d.rows.map(row => {
    const tr = h("tr", { tabindex: 0, title: `Session history of ${row.username}` },
      LS_COLS.map(([k, , n]) => h("td", { class: n ? "num" : null },
        k === "state" ? stateTag(row.state) : row.state === "none" && n ? "" : (row[k] ?? ""))));
    const open = () => openHistory(row.username);
    tr.addEventListener("click", open);
    tr.addEventListener("keydown", e => { if (e.key === "Enter") open(); });
    return tr;
  }));
  if (!d.rows.length) {
    $("#ls-table tbody").append(h("tr", {}, h("td", { colspan: LS_COLS.length, class: "muted" }, "No users match.")));
  }
  const pages = Math.max(1, Math.ceil(d.total / d.page_size));
  $("#ls-page-info").textContent = `Page ${d.page} of ${pages} · ${fmt(d.total)} user(s)`;
  $("#ls-prev").disabled = d.page <= 1;
  $("#ls-next").disabled = d.page >= pages;
}

// ---- session history modal -------------------------------------------------

const HIST_COLS = [
  ["start", "Start"], ["stop", "Stop"], ["duration_h", "Duration"], ["ip", "IP address"],
  ["mac", "MAC"], ["nas_ip", "NAS IP"], ["called_station", "Called station"], ["nas_port", "NAS port"],
  ["upload_h", "Upload", 1], ["download_h", "Download", 1], ["total_h", "Total", 1],
  ["terminate_cause", "Stop reason"]];

// cursors: the SAS4 page each modal page started at, so Newer can go back.
S.hist = { username: null, cursors: [1], next: null, seq: 0 };

function openHistory(username) {
  S.hist = { username, cursors: [1], next: null, seq: S.hist.seq };
  $("#hist-title").textContent = username;
  $("#hist-sub").textContent = "";
  $("#history").showModal();
  loadHistory();
}

async function loadHistory() {
  const hs = S.hist, seq = ++hs.seq, err = $("#hist-error");
  err.hidden = true;
  $("#hist-older").disabled = $("#hist-newer").disabled = true;
  $("#hist-table thead").replaceChildren(h("tr", {}, HIST_COLS.map(([, l, n]) => h("th", { class: n ? "num" : null }, l))));
  $("#hist-table tbody").replaceChildren(h("tr", {}, h("td", { colspan: HIST_COLS.length, class: "muted" }, "Loading…")));
  try {
    const d = await api("/api/sessions/history",
      { body: { username: hs.username, cursor: hs.cursors[hs.cursors.length - 1] } });
    if (seq !== S.hist.seq) return;             // closed or moved on
    hs.next = d.next;
    const u = d.user || {};
    $("#hist-sub").textContent = [[u.firstname, u.lastname].filter(Boolean).join(" "), u.profile_name,
      u.parent_username && `manager ${u.parent_username}`, u.expiration && `expires ${u.expiration}`,
      u.phone].filter(Boolean).join(" · ");
    $("#hist-table tbody").replaceChildren(...d.rows.map(row => h("tr", {},
      HIST_COLS.map(([k, , n]) => h("td", { class: n ? "num" : null },
        k === "stop" && row.online ? stateTag("online") : (row[k] ?? ""))))));
    if (!d.rows.length) {
      $("#hist-table tbody").append(h("tr", {}, h("td", { colspan: HIST_COLS.length, class: "muted" },
        hs.cursors.length > 1 ? "No older sessions." : "No sessions found for this user.")));
    }
    $("#hist-info").textContent = `Page ${hs.cursors.length} · ${fmt(d.rows.length)} session(s)`;
    $("#hist-newer").disabled = hs.cursors.length <= 1;
    $("#hist-older").disabled = !d.next;
  } catch (ex) {
    if (seq !== S.hist.seq) return;
    err.textContent = ex.message; err.hidden = false;
    $("#hist-table tbody").replaceChildren();
    $("#hist-newer").disabled = hs.cursors.length <= 1;
  }
}

$("#hist-older").addEventListener("click", () => { S.hist.cursors.push(S.hist.next); loadHistory(); });
$("#hist-newer").addEventListener("click", () => { S.hist.cursors.pop(); loadHistory(); });
$("#history").addEventListener("close", () => { S.hist.seq++; });

// ---- charts (plain SVG) -----------------------------------------------------

const SVGNS = "http://www.w3.org/2000/svg";
function svg(tag, attrs = {}, ...kids) {
  const el = document.createElementNS(SVGNS, tag);
  for (const [k, v] of Object.entries(attrs)) if (v !== null && v !== undefined) el.setAttribute(k, v);
  for (const k of kids.flat()) if (k) el.append(k instanceof Node ? k : document.createTextNode(String(k)));
  return el;
}

function niceMax(v) {
  if (v <= 0) return 4;
  const mag = 10 ** Math.floor(Math.log10(v)), n = v / mag;
  return (n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : 10) * mag;
}
const short = n => n >= 1e6 ? `${+(n / 1e6).toFixed(1)}M` : n >= 1e4 ? `${+(n / 1e3).toFixed(0)}k` : fmt(n);

// Shared frame: hairline grid, y ticks, a few x labels, a tooltip box.
function frame(el, labels, maxValue) {
  el.replaceChildren();
  const W = Math.max(320, el.clientWidth), H = 240;
  const m = { l: 48, r: 12, t: 10, b: 26 };
  const pw = W - m.l - m.r, ph = H - m.t - m.b;
  const top = niceMax(maxValue);
  const root = svg("svg", { viewBox: `0 0 ${W} ${H}`, height: H, role: "img" });
  for (let i = 0; i <= 4; i++) {
    const v = (top / 4) * i, y = m.t + ph - (ph * v) / top;
    root.append(svg("line", { class: i ? "gridline" : "baseline", x1: m.l, x2: W - m.r, y1: y, y2: y }),
                svg("text", { class: "tick", x: m.l - 6, y: y + 4, "text-anchor": "end" }, short(v)));
  }
  const step = Math.max(1, Math.ceil(labels.length / Math.max(2, Math.floor(pw / 80))));
  const band = pw / labels.length;
  labels.forEach((lab, i) => {
    if (i % step) return;
    root.append(svg("text", { class: "tick", x: m.l + band * (i + 0.5), y: H - 8, "text-anchor": "middle" }, lab));
  });
  const tip = h("div", { class: "tip", hidden: true });
  el.append(root, tip);
  return { root, tip, W, H, m, pw, ph, top, band,
           y: v => m.t + ph - (ph * v) / top, x: i => m.l + band * (i + 0.5) };
}

function placeTip(f, el, px, content) {
  f.tip.replaceChildren(...content);
  f.tip.hidden = false;
  const box = f.tip.getBoundingClientRect(), scale = el.clientWidth / f.W;
  let left = px * scale + 12;
  if (left + box.width > el.clientWidth) left = px * scale - box.width - 12;
  f.tip.style.left = `${Math.max(0, left)}px`;
  f.tip.style.top = `${f.m.t}px`;
}

function lineChart(el, labels, values, name) {
  const real = values.filter(v => v !== null);
  const f = frame(el, labels, real.length ? Math.max(...real) : 0);
  // One path, broken where a value is missing (future periods).
  let d = "", pen = false;
  values.forEach((v, i) => {
    if (v === null) { pen = false; return; }
    d += `${pen ? "L" : "M"}${f.x(i).toFixed(1)},${f.y(v).toFixed(1)}`;
    pen = true;
  });
  f.root.append(svg("path", { class: "line", d }));
  const lastI = values.reduce((acc, v, i) => (v !== null ? i : acc), -1);
  if (lastI >= 0) f.root.append(svg("circle", { class: "dot", cx: f.x(lastI), cy: f.y(values[lastI]), r: 4 }));
  // Crosshair + tooltip on hover.
  const cross = svg("line", { class: "crosshair", y1: f.m.t, y2: f.m.t + f.ph, visibility: "hidden" });
  const hot = svg("circle", { class: "dot", r: 5, visibility: "hidden" });
  const hit = svg("rect", { x: f.m.l, y: f.m.t, width: f.pw, height: f.ph, fill: "transparent" });
  f.root.append(cross, hot, hit);
  const move = e => {
    const pt = e.offsetX * (f.W / el.clientWidth);
    const i = Math.min(labels.length - 1, Math.max(0, Math.floor((pt - f.m.l) / f.band)));
    const x = f.x(i), v = values[i];
    cross.setAttribute("x1", x); cross.setAttribute("x2", x); cross.setAttribute("visibility", "visible");
    if (v === null) hot.setAttribute("visibility", "hidden");
    else { hot.setAttribute("cx", x); hot.setAttribute("cy", f.y(v)); hot.setAttribute("visibility", "visible"); }
    placeTip(f, el, x, [h("b", {}, labels[i]), h("br"), `${name}: ${v === null ? "— (future)" : fmt(v)}`]);
  };
  hit.addEventListener("mousemove", move);
  hit.addEventListener("mouseleave", () => { f.tip.hidden = true; cross.setAttribute("visibility", "hidden"); hot.setAttribute("visibility", "hidden"); });
}

function barChart(el, labels, series) {
  const f = frame(el, labels, Math.max(0, ...series.flatMap(s => s.values)));
  const gap = 2, n = series.length;
  const bw = Math.max(1, Math.min(24, (f.band * 0.8 - gap * (n - 1)) / n));
  const group = bw * n + gap * (n - 1);
  const hover = svg("rect", { class: "band-hover", y: f.m.t, height: f.ph, width: f.band, visibility: "hidden" });
  f.root.append(hover);
  // Bars: 4px rounded data-end, square on the baseline.
  labels.forEach((_, i) => {
    series.forEach((s, k) => {
      const v = s.values[i];
      if (!v) return;
      const x = f.x(i) - group / 2 + k * (bw + gap), y = f.y(v), base = f.y(0);
      const r = Math.min(4, bw / 2, base - y);
      f.root.append(svg("path", { class: s.cls,
        d: `M${x},${base}V${y + r}Q${x},${y} ${x + r},${y}H${x + bw - r}Q${x + bw},${y} ${x + bw},${y + r}V${base}Z` }));
    });
  });
  const hit = svg("rect", { x: f.m.l, y: f.m.t, width: f.pw, height: f.ph, fill: "transparent" });
  f.root.append(hit);
  hit.addEventListener("mousemove", e => {
    const pt = e.offsetX * (f.W / el.clientWidth);
    const i = Math.min(labels.length - 1, Math.max(0, Math.floor((pt - f.m.l) / f.band)));
    hover.setAttribute("x", f.m.l + f.band * i);
    hover.setAttribute("visibility", "visible");
    placeTip(f, el, f.x(i), [h("b", {}, labels[i]),
      ...series.flatMap(s => [h("br"), h("span", { class: `swatch ${s.sw}` }), `${s.name}: ${fmt(s.values[i])}`,
                              s.note && s.note[i] ? ` (${s.note[i]})` : ""])]);
  });
  hit.addEventListener("mouseleave", () => { f.tip.hidden = true; hover.setAttribute("visibility", "hidden"); });
}

boot();
