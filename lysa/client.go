// Package lysa is a thin client for Lysa's undocumented internal API
// (api.lysa.se), covering the BankID QR login flow and the read-only data
// endpoints used to export your own account data.
//
// Auth model: BankID login yields a short-lived `lysa-token` JWT cookie
// (~30 min). This client tracks that token manually and replays it as a Cookie
// header on data calls. `x-app-id` is a cosmetic per-session id, not validated
// against the token. The whole export runs in a couple of minutes, well inside
// the token's lifetime, so there's no session-refresh machinery.
package lysa

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	BaseURL = "https://api.lysa.se"
	Origin  = "https://app.lysa.se"
	// UserAgent mimics a current Google Chrome on Windows so requests look like
	// the real web app rather than a bare HTTP client.
	UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
)

// Client talks to api.lysa.se. Safe for concurrent use.
type Client struct {
	http  *http.Client
	appID string

	mu    sync.Mutex
	token string
}

// New returns a client.
func New() *Client {
	return &Client{
		http:  &http.Client{Timeout: 30 * time.Second},
		appID: "00000000-0000-0000-0000-000000000000", // cosmetic; not validated
	}
}

func (c *Client) getToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

func (c *Client) captureToken(resp *http.Response) {
	for _, ck := range resp.Cookies() {
		if ck.Name == "lysa-token" && ck.Value != "" {
			c.mu.Lock()
			c.token = ck.Value
			c.mu.Unlock()
		}
	}
}

// Authed reports whether a login token has been captured.
func (c *Client) Authed() bool { return c.getToken() != "" }

func (c *Client) request(ctx context.Context, method, path string) ([]byte, *http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, BaseURL+path, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-app-id", c.appID)
	req.Header.Set("Origin", Origin)
	req.Header.Set("Referer", Origin+"/")
	req.Header.Set("User-Agent", UserAgent)
	// Chrome client-hint + fetch-metadata headers, to match the real browser.
	req.Header.Set("sec-ch-ua", `"Chromium";v="140", "Google Chrome";v="140", "Not?A_Brand";v="24"`)
	req.Header.Set("sec-ch-ua-mobile", "?0")
	req.Header.Set("sec-ch-ua-platform", `"Windows"`)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	if tok := c.getToken(); tok != "" {
		req.Header.Set("Cookie", "lysa-token="+tok+"; logged-in=true")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp, err
	}
	if resp.StatusCode != http.StatusOK {
		return body, resp, fmt.Errorf("%s %s -> HTTP %d: %s", method, path, resp.StatusCode, truncate(body, 200))
	}
	return body, resp, nil
}

// --- BankID login flow ---

// StartLogin begins a BankID order and returns its orderRef.
func (c *Client) StartLogin(ctx context.Context) (orderRef string, err error) {
	body, _, err := c.request(ctx, "POST", "/bankid/login")
	if err != nil {
		return "", err
	}
	var r struct {
		OrderRef string `json:"orderRef"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", err
	}
	if r.OrderRef == "" {
		return "", fmt.Errorf("no orderRef in login response: %s", truncate(body, 200))
	}
	return r.OrderRef, nil
}

// QRCode fetches the current animated-QR payload string for an order. Lysa
// computes the BankID HMAC server-side; we just render the returned string.
func (c *Client) QRCode(ctx context.Context, orderRef string) (string, error) {
	body, _, err := c.request(ctx, "GET", "/bankid/login/qr/"+url.PathEscape(orderRef))
	if err != nil {
		return "", err
	}
	var r struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", err
	}
	return r.Code, nil
}

// Collect polls login status. On "complete" it captures the lysa-token cookie
// from the Set-Cookie header.
func (c *Client) Collect(ctx context.Context, orderRef string) (status, hintCode string, err error) {
	body, resp, err := c.request(ctx, "GET", "/bankid/login/"+url.PathEscape(orderRef))
	if err != nil {
		return "", "", err
	}
	var r struct {
		Status   string `json:"status"`
		HintCode string `json:"hintCode"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", "", err
	}
	if r.Status == "complete" {
		c.captureToken(resp)
		if !c.Authed() {
			return "", "", fmt.Errorf("login complete but no lysa-token cookie was set")
		}
	}
	return r.Status, r.HintCode, nil
}

// --- data endpoints (raw JSON) ---

// Data-endpoint paths. Each of these string literals also appears verbatim in
// Lysa's public SPA bundle, so CheckAPI can detect when the API moves. The
// methods below and DataPaths both reference these constants — keep them the
// single source of truth so the preflight check can't drift from what we call.
const (
	pathAccountsAll  = "/accounts/all"
	pathTransactions = "/transactions"
	pathPerformance  = "/accounts/performance"
	pathLegalEntity  = "/legal-entity"
	pathAdvice       = "/investments/combined/advice"
	pathFeesPaid     = "/fees/paid"
	pathFundsSummary = "/funds/data/summary"
	pathTaxIskYears  = "/tax/isk/years"
	pathDocuments    = "/documents"
)

// DataPaths is the set of path literals CheckAPI verifies against the SPA
// bundle. The BankID login paths are deliberately excluded — they are assembled
// from fragments in the SPA (base + `/login/${ref}`) and never appear as
// contiguous strings, so a substring check on them would always false-positive.
var DataPaths = []string{
	pathAccountsAll, pathTransactions, pathPerformance, pathLegalEntity,
	pathAdvice, pathFeesPaid, pathFundsSummary, pathTaxIskYears, pathDocuments,
}

func (c *Client) get(ctx context.Context, path string) (json.RawMessage, error) {
	body, _, err := c.request(ctx, "GET", path)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

func (c *Client) AccountsAll(ctx context.Context) (json.RawMessage, error) {
	return c.get(ctx, pathAccountsAll)
}

func (c *Client) Transactions(ctx context.Context, from, to string) (json.RawMessage, error) {
	return c.get(ctx, pathTransactions+"?from="+url.QueryEscape(from)+"&to="+url.QueryEscape(to))
}

func (c *Client) Performance(ctx context.Context, start, end string) (json.RawMessage, error) {
	return c.get(ctx, pathPerformance+"?start="+url.QueryEscape(start)+"&end="+url.QueryEscape(end))
}

func (c *Client) LegalEntity(ctx context.Context) (json.RawMessage, error) {
	return c.get(ctx, pathLegalEntity)
}

func (c *Client) Advice(ctx context.Context) (json.RawMessage, error) {
	return c.get(ctx, pathAdvice)
}

func (c *Client) FeesPaid(ctx context.Context) (json.RawMessage, error) {
	return c.get(ctx, pathFeesPaid)
}

func (c *Client) FundsSummary(ctx context.Context) (json.RawMessage, error) {
	return c.get(ctx, pathFundsSummary)
}

func (c *Client) TaxIskYears(ctx context.Context) (json.RawMessage, error) {
	return c.get(ctx, pathTaxIskYears)
}

func (c *Client) Documents(ctx context.Context) (json.RawMessage, error) {
	return c.get(ctx, pathDocuments)
}

func truncate(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		return s[:n]
	}
	return s
}
