// Command lysa-export runs a tiny local web app that logs into Lysa with
// BankID (QR), lets you pick which of your datasets to export, writes them to
// disk as JSON + CSV, and then exits.
//
// Personal, read-only use of your own account against Lysa's undocumented
// internal API. See README.md.
package main

import (
	_ "embed"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/rbnkr/lysa-export/lysa"
	qrcode "github.com/skip2/go-qrcode"
)

//go:embed web/index.html
var indexHTML []byte

//go:embed web/viewer.html
var viewerHTML []byte

// defaultBuildHash is the SPA `hash` value observed on api.lysa.se login calls.
// If login starts 4xx-ing after a Lysa frontend deploy, grab a fresh hash from
// any api.lysa.se request URL and pass it via LYSA_BUILD_HASH.
const defaultBuildHash = "b7e94f4ae4fd2168e99698ee359ba6b96332ca39"

type server struct {
	outDir string

	mu       sync.Mutex
	client   *lysa.Client
	orderRef string
}

func main() {
	srv := &server{outDir: env("OUT_DIR", "/out")}
	port := env("PORT", "8080")

	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.handleIndex)
	mux.HandleFunc("/api/datasets", srv.handleDatasets)
	mux.HandleFunc("/api/auth/start", srv.handleStart)
	mux.HandleFunc("/api/auth/qr.png", srv.handleQR)
	mux.HandleFunc("/api/auth/status", srv.handleStatus)
	mux.HandleFunc("/api/export", srv.handleExport)

	log.Printf("lysa-export listening on :%s — open http://localhost:%s (writing to %s)", port, port, srv.outDir)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func (s *server) handleDatasets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, lysa.Datasets)
}

func (s *server) handleStart(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Fresh client + order each time Start is called (e.g. after QR expiry).
	c := lysa.New(env("LYSA_BUILD_HASH", defaultBuildHash))
	orderRef, err := c.StartLogin(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	s.client = c
	s.orderRef = orderRef
	writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
}

func (s *server) handleQR(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	c, orderRef := s.client, s.orderRef
	s.mu.Unlock()
	if c == nil || orderRef == "" {
		http.Error(w, "no login in progress", http.StatusConflict)
		return
	}
	code, err := c.QRCode(r.Context(), orderRef)
	if err != nil {
		// Order likely expired; tell the client to restart.
		http.Error(w, err.Error(), http.StatusGone)
		return
	}
	png, err := qrcode.Encode(code, qrcode.Medium, 320)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(png)
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	c, orderRef := s.client, s.orderRef
	s.mu.Unlock()
	if c == nil || orderRef == "" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "idle"})
		return
	}
	status, hint, err := c.Collect(r.Context(), orderRef)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status, "hintCode": hint})
}

func (s *server) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	c := s.client
	s.mu.Unlock()
	if c == nil || !c.Authed() {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}
	var req struct {
		Datasets []string `json:"datasets"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
		return
	}
	dir, files, err := c.Export(r.Context(), s.outDir, req.Datasets, string(viewerHTML))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "files": files})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files, "outDir": dir})

	// Job done: exit so the container stops.
	go func() {
		time.Sleep(1500 * time.Millisecond)
		log.Printf("export complete (%d files) — exiting", len(files))
		os.Exit(0)
	}()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
