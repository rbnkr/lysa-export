package lysa

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// AppBaseURL is the public Lysa web app (the SPA), separate from the API host.
const AppBaseURL = "https://app.lysa.se"

// assetRe matches the hashed main bundle reference in the SPA's index HTML,
// e.g. /assets/index-BfLJI7Xw.js. The filename hash rotates on every deploy, so
// we always resolve the current one from the index rather than hardcoding it.
var assetRe = regexp.MustCompile(`/assets/index-[A-Za-z0-9_-]+\.js`)

// CheckAPI fetches Lysa's public SPA bundle and returns any DataPaths that no
// longer appear in it — a signal the API surface moved and the exporter needs
// updating. It requires no authentication.
//
// It is intentionally fail-open: any network error, non-200, or parse failure
// returns nil (no drift reported). A Lysa outage, a bundle restructure, or a
// minifier change must never block a login/export that would otherwise work —
// this is an advisory heads-up, not a gate. Only data-endpoint literals are
// checked; see DataPaths for why the login paths are excluded.
func CheckAPI() (missing []string) {
	client := &http.Client{Timeout: 20 * time.Second}

	index, err := fetchPublic(client, AppBaseURL+"/")
	if err != nil {
		return nil
	}
	asset := assetRe.Find(index)
	if asset == nil {
		return nil
	}
	bundle, err := fetchPublic(client, AppBaseURL+string(asset))
	if err != nil {
		return nil
	}

	js := string(bundle)
	for _, p := range DataPaths {
		if !strings.Contains(js, p) {
			missing = append(missing, p)
		}
	}
	return missing
}

func fetchPublic(c *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s -> HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
