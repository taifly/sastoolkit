// Package sas4 is the SAS4 admin API client.
//
// Routes and bodies come from the SAS4 developer collection
// (https://documenter.getpostman.com/view/11765341/U16byA2y). Every request
// body is JSON, AES-256-CBC encrypted in the CryptoJS/OpenSSL "Salted__"
// envelope and sent as {"payload": "<base64>"}.
package sas4

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"sastoolkit/internal/val"
)

type M = val.M

var browserHeaders = map[string]string{
	"Accept":          "application/json, text/plain, */*",
	"Accept-Language": "en-US,en;q=0.9",
	"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) " +
		"Chrome/151.0.0.0 Safari/537.36",
}

// --------------------------------------------------------------------------
// RESPONSES
// --------------------------------------------------------------------------

// Succeeded: a 2xx with a JSON object whose own status (if any) is a success
// and that carries no error.
func Succeeded(status int, data any) bool {
	obj, ok := data.(M)
	if status < 200 || status >= 300 || !ok {
		return false
	}
	if inner, has := obj["status"]; has && inner != nil {
		switch val.Str(inner) {
		case "200", "true", "True", "1":
		default:
			return false
		}
	}
	return !val.Truthy(obj["error"]) && !val.Truthy(obj["errors"])
}

// Message is the most useful line of a response, for reports.
func Message(data any) string {
	if obj, ok := data.(M); ok {
		for _, key := range []string{"message", "error", "errors", "raw"} {
			if val.Truthy(obj[key]) {
				return val.Cut(val.Str(obj[key]), 300)
			}
		}
	}
	return val.Cut(val.Dumps(data), 300)
}

type LoginError struct{ Msg string }

func (e *LoginError) Error() string { return e.Msg }

// --------------------------------------------------------------------------
// CLIENT
// --------------------------------------------------------------------------

type Options struct {
	ConnectTimeout time.Duration // default 10s
	ReadTimeout    time.Duration // default 120s, the whole request
	Insecure       bool
	Secret         string
}

type Client struct {
	Origin   string
	Username string
	password string
	base     string
	headers  map[string]string
	secret   string
	http     *http.Client

	mu      sync.Mutex // guards token
	token   string
	loginMu sync.Mutex
}

// New takes base_url as 'https://bss.libonet.ly' -- scheme and host only.
func New(baseURL, username, password string, opt Options) *Client {
	if opt.ConnectTimeout == 0 {
		opt.ConnectTimeout = 10 * time.Second
	}
	if opt.ReadTimeout == 0 {
		opt.ReadTimeout = 120 * time.Second
	}
	if opt.Secret == "" {
		opt.Secret = APISecret
	}
	origin := strings.TrimRight(baseURL, "/")
	headers := map[string]string{"Origin": origin, "Referer": origin + "/"}
	for k, v := range browserHeaders {
		headers[k] = v
	}
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: opt.ConnectTimeout}).DialContext,
		TLSHandshakeTimeout: opt.ConnectTimeout,
		MaxIdleConnsPerHost: 32,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: opt.Insecure},
	}
	return &Client{
		Origin: origin, Username: username, password: password,
		base:    origin + "/admin/api/index.php/api/",
		headers: headers, secret: opt.Secret,
		http: &http.Client{Transport: transport, Timeout: opt.ReadTimeout},
	}
}

func (c *Client) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// send makes one request with the given token. err is only set when nothing
// came back.
func (c *Client) send(method, route, token string, payload any) (int, any, error) {
	var body io.Reader
	if payload != nil {
		plain, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		enc, err := Encrypt(string(plain), c.secret)
		if err != nil {
			return 0, nil, err
		}
		wrapped, _ := json.Marshal(M{"payload": enc})
		body = bytes.NewReader(wrapped)
	}
	req, err := http.NewRequest(method, c.base+route, body)
	if err != nil {
		return 0, nil, err
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var data any
	if err := dec.Decode(&data); err != nil {
		return resp.StatusCode, M{"raw": val.Cut(string(raw), 400)}, nil
	}
	return resp.StatusCode, data, nil
}

// Login gets a fresh token.
func (c *Client) Login() error { return c.relogin(c.currentToken()) }

// relogin replaces the token `stale`. Concurrent callers rejected with the
// same token log in once between them.
func (c *Client) relogin(stale string) error {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	if stale != "" && c.currentToken() != stale {
		return nil // another goroutine already renewed it
	}
	status, data, err := c.send("POST", "login", "", M{
		"username": c.Username, "password": c.password, "language": "en"})
	if err != nil {
		return &LoginError{fmt.Sprintf("cannot reach %s: %v", c.Origin, err)}
	}
	var token string
	if obj, ok := data.(M); ok {
		token = val.Str(obj["token"])
	}
	if token == "" {
		return &LoginError{fmt.Sprintf("login failed (HTTP %d): %s", status,
			Message(data))}
	}
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
	return nil
}

// Call makes one request. A 401 always logs in again and resends -- the
// server rejected it before doing anything. Network errors and 5xx are only
// retried when `retries` allows it, which callers set for reads only: a write
// that timed out may already have been applied.
func (c *Client) Call(method, route string, payload any, retries int) (int, any) {
	relogged := false
	attempt := 0
	for {
		token := c.currentToken()
		status, data, err := c.send(method, route, token, payload)
		if err != nil {
			if attempt >= retries {
				return 0, M{"error": fmt.Sprintf("%T: %v", err, err)}
			}
			attempt++
			time.Sleep(time.Duration(1<<attempt) * time.Second)
			continue
		}
		if status == 401 && !relogged {
			relogged = true
			if err := c.relogin(token); err != nil {
				return 401, M{"error": err.Error()}
			}
			continue
		}
		if status >= 500 && attempt < retries {
			attempt++
			time.Sleep(time.Duration(1<<attempt) * time.Second)
			continue
		}
		return status, data
	}
}

// -- reads (retried) --------------------------------------------------------

func (c *Client) Get(route string) (int, any) { return c.Call("GET", route, nil, 3) }

func (c *Client) ReadPost(route string, payload any) (int, any) {
	return c.Call("POST", route, payload, 3)
}

type PageOpts struct {
	PageSize  int
	Search    string
	Retries   int
	SortBy    string
	Direction string
	// Extra fields sent with every page, e.g. bulk_filter.
	Extra M
	// OnPage(page, lastPage, seen, total) after each page.
	OnPage func(page int, last any, seen int, total any)
}

// DefaultPageOpts are the Python defaults: 500 a page, 3 retries, id asc.
func DefaultPageOpts() PageOpts {
	return PageOpts{PageSize: 500, Retries: 3, SortBy: "id", Direction: "asc"}
}

// Paged calls fn with every record of an index/* listing, page by page,
// until fn returns false or the listing ends.
func (c *Client) Paged(route string, columns []string, opt PageOpts,
	fn func(M) bool) error {
	page, seen := 1, 0
	for {
		body := M{"page": page, "count": opt.PageSize, "sortBy": opt.SortBy,
			"direction": opt.Direction, "search": opt.Search,
			"columns": columns}
		for k, v := range opt.Extra {
			body[k] = v
		}
		status, data := c.Call("POST", route, body, opt.Retries)
		obj, _ := data.(M)
		rows, ok := obj["data"].([]any)
		if !ok {
			return fmt.Errorf("%s failed on page %d (HTTP %d): %s", route, page,
				status, Message(data))
		}
		if len(rows) == 0 {
			return nil
		}
		for _, r := range rows {
			rec, _ := r.(M)
			if rec == nil {
				rec = M{}
			}
			if !fn(rec) {
				return nil
			}
		}
		seen += len(rows)
		last := obj["last_page"]
		if opt.OnPage != nil {
			opt.OnPage(page, last, seen, obj["total"])
		}
		if n, ok := val.Int(last); (ok && n != 0 && page >= n) ||
			len(rows) < opt.PageSize {
			return nil
		}
		page++
	}
}

func dataList(data any) []any {
	if obj, ok := data.(M); ok {
		rows, _ := obj["data"].([]any)
		return rows
	}
	rows, _ := data.([]any)
	return rows
}

func (c *Client) Profiles() ([]M, error) {
	status, data := c.Get("list/profile/0")
	obj, _ := data.(M)
	rows, ok := obj["data"].([]any)
	if !ok {
		return nil, fmt.Errorf("cannot list profiles (HTTP %d): %s", status,
			Message(data))
	}
	out := []M{}
	for _, r := range rows {
		p, _ := r.(M)
		id, ok := val.Int(p["id"])
		if !ok {
			return nil, fmt.Errorf("profile without an id: %s", val.Dumps(p))
		}
		out = append(out, M{"id": id, "name": p["name"]})
	}
	return out, nil
}

func (c *Client) Sites() []M {
	_, data := c.Get("site")
	out := []M{}
	for _, r := range dataList(data) {
		s, ok := r.(M)
		if !ok {
			continue
		}
		if id, ok := val.Int(s["id"]); ok {
			out = append(out, M{"id": id, "name": s["name"]})
		}
	}
	return out
}

func (c *Client) AllowedExtensions(profileID int) []M {
	_, data := c.Get(fmt.Sprintf("allowedExtensions/%d", profileID))
	out := []M{}
	for _, r := range dataList(data) {
		if m, ok := r.(M); ok {
			out = append(out, m)
		}
	}
	return out
}
