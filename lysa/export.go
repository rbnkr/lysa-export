package lysa

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Dataset is a selectable export.
type Dataset struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Desc  string `json:"desc"`
}

// Datasets is the list offered in the UI, in display order.
var Datasets = []Dataset{
	{"accounts", "Accounts & holdings", "Current positions per account (ISIN, volume, worth, average cost), incl. savings accounts"},
	{"transactions", "Transaction history", "Every transaction (buys, sells, deposits, fees, switches) with ISINs"},
	{"performance", "Performance", "Daily portfolio value series + total return"},
	{"profile", "Profile", "Legal entity / KYC (name, personal number, email)"},
	{"advice", "Advice & risk", "Per-account advised vs taken risk and suitability declaration"},
	{"fees", "Fees paid", "Every management fee charged (date, amount, VAT) + current fee rates"},
	{"funds", "Fund catalog", "The funds you hold — ISIN, name, share class + full look-through holdings"},
	{"tax", "Tax years", "Available ISK tax (deklaration) years per account + per-year detail"},
	{"documents", "Documents", "Register of your statements & documents"},
}

func validKey(k string) bool {
	for _, d := range Datasets {
		if d.Key == k {
			return true
		}
	}
	return false
}

// datasetSpec describes one exportable dataset: how to fetch its raw JSON and,
// optionally, how to flatten it into a CSV.
type datasetSpec struct {
	key    string // dataset key, JSON basename, and viewer data key
	fetch  func() (json.RawMessage, error)
	csv    string // CSV basename; empty = JSON only
	header []string
	rows   func(json.RawMessage) ([][]string, error)
}

// rowsFn adapts a typed row-builder into one that unmarshals raw JSON first, so
// the dataset table can stay uniform regardless of each response's Go type.
func rowsFn[T any](rows func(T) [][]string) func(json.RawMessage) ([][]string, error) {
	return func(raw json.RawMessage) ([][]string, error) {
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		return rows(v), nil
	}
}

// constRaw returns a fetch func that yields already-fetched raw JSON.
func constRaw(raw json.RawMessage) func() (json.RawMessage, error) {
	return func() (json.RawMessage, error) { return raw, nil }
}

// Build fetches the selected datasets and returns the export as an in-memory set
// of basename -> file bytes: pretty JSON (always) plus flattened CSV (for
// accounts/transactions/performance/fees/funds/tax) and, when viewerTmpl is
// non-empty, the self-contained viewer.html. It performs no disk I/O — the
// caller decides whether to Zip it, WriteDir it, or both.
func (c *Client) Build(ctx context.Context, selected []string, viewerTmpl string) (map[string][]byte, error) {
	if !c.Authed() {
		return nil, fmt.Errorf("not authenticated")
	}
	sel := map[string]bool{}
	for _, s := range selected {
		if validKey(s) {
			sel[s] = true
		}
	}
	if len(sel) == 0 {
		return nil, fmt.Errorf("no valid datasets selected")
	}

	today := time.Now().UTC().Format("2006-01-02")
	const earliest = "2000-01-01"

	files := map[string][]byte{}
	raws := map[string]json.RawMessage{}
	addJSON := func(name string, raw json.RawMessage) error {
		raws[name] = raw
		var pretty any
		if err := json.Unmarshal(raw, &pretty); err != nil {
			return err
		}
		b, _ := json.MarshalIndent(pretty, "", "  ")
		files[name+".json"] = b
		return nil
	}
	addCSV := func(name string, header []string, rows [][]string) error {
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		_ = w.Write(header)
		_ = w.WriteAll(rows)
		w.Flush()
		if err := w.Error(); err != nil {
			return err
		}
		files[name+".csv"] = buf.Bytes()
		return nil
	}

	// accounts/all is fetched up front when accounts, performance, or fees is
	// selected: it's a dataset in its own right, supplies the earliest
	// account-creation date used as the performance series start, and lists
	// the account ids the per-account fee-rate calls need.
	var accountsRaw json.RawMessage
	var accs accountsResp
	perfStart := earliest
	if sel["accounts"] || sel["performance"] || sel["fees"] {
		raw, err := c.AccountsAll(ctx)
		if err != nil {
			return nil, err
		}
		accountsRaw = raw
		if err := json.Unmarshal(raw, &accs); err != nil {
			return nil, err
		}
		if e := accs.earliestCreated(); e != "" {
			perfStart = e
		}
	}

	specs := []datasetSpec{
		{key: "accounts", fetch: constRaw(accountsRaw), csv: "positions", header: positionsHeader, rows: rowsFn(func(a accountsResp) [][]string { return a.positionRows() })},
		{key: "transactions", fetch: func() (json.RawMessage, error) { return c.Transactions(ctx, earliest, today) }, csv: "transactions", header: txHeader, rows: rowsFn(txRows)},
		{key: "performance", fetch: func() (json.RawMessage, error) { return c.Performance(ctx, perfStart, today) }, csv: "performance", header: perfHeader, rows: rowsFn(func(p performanceResp) [][]string { return p.rows() })},
		{key: "profile", fetch: func() (json.RawMessage, error) { return c.LegalEntity(ctx) }},
		{key: "advice", fetch: func() (json.RawMessage, error) { return c.Advice(ctx) }},
		{key: "fees", fetch: func() (json.RawMessage, error) { return c.FeesPaid(ctx) }, csv: "fees", header: feesHeader, rows: rowsFn(feeRows)},
		{key: "funds", fetch: func() (json.RawMessage, error) { return c.FundsSummary(ctx) }, csv: "funds", header: fundsHeader, rows: rowsFn(fundRows)},
		{key: "tax", fetch: func() (json.RawMessage, error) { return c.TaxIskYears(ctx) }, csv: "tax_years", header: taxHeader, rows: rowsFn(taxRows)},
		{key: "documents", fetch: func() (json.RawMessage, error) { return c.Documents(ctx) }},
	}

	for _, s := range specs {
		if !sel[s.key] {
			continue
		}
		raw, err := s.fetch()
		if err != nil {
			return nil, err
		}
		if err := addJSON(s.key, raw); err != nil {
			return nil, err
		}
		if s.rows != nil {
			rows, err := s.rows(raw)
			if err != nil {
				return nil, err
			}
			if err := addCSV(s.csv, s.header, rows); err != nil {
				return nil, err
			}
		}
	}

	// --- best-effort extras ---
	// Deeper data behind endpoints spotted in the SPA bundle but not yet
	// probed against a live session. Errors are embedded in the output file
	// instead of failing the export, so a moved/changed endpoint costs
	// nothing but a visible note in the extra file itself.

	// Per-year ISK deklaration data, one call per account × tax year.
	if raw, ok := raws["tax"]; ok {
		var years []taxIsk
		if err := json.Unmarshal(raw, &years); err == nil {
			type iskDetail struct {
				AccountID string          `json:"accountId"`
				TaxYear   int             `json:"taxYear"`
				Data      json.RawMessage `json:"data,omitempty"`
				Error     string          `json:"error,omitempty"`
			}
			var details []iskDetail
			for _, t := range years {
				for _, y := range t.TaxYears {
					d := iskDetail{AccountID: t.AccountID, TaxYear: y}
					if data, err := c.TaxIsk(ctx, strconv.Itoa(y), t.AccountID); err != nil {
						d.Error = err.Error()
					} else {
						d.Data = data
					}
					details = append(details, d)
				}
			}
			if len(details) > 0 {
				b, _ := json.Marshal(details)
				if err := addJSON("tax_isk", b); err != nil {
					return nil, err
				}
			}
		}
	}

	// Full look-through holdings per fund. One representative share class per
	// depot — classes of the same depot share the same underlying holdings.
	if raw, ok := raws["funds"]; ok {
		var depots []fundsDepot
		if err := json.Unmarshal(raw, &depots); err == nil {
			var isins []string
			for _, d := range depots {
				if len(d.FundShareClasses) > 0 && d.FundShareClasses[0].ISIN != "" {
					isins = append(isins, d.FundShareClasses[0].ISIN)
				}
			}
			if len(isins) > 0 {
				out, err := c.FundsHoldings(ctx, isins)
				if err != nil {
					out, _ = json.Marshal(map[string]string{"error": err.Error()})
				}
				if err := addJSON("fund_holdings", out); err != nil {
					return nil, err
				}
			}
		}
	}

	// Current fee rates, one call per investment account.
	if sel["fees"] && len(accs.InvestmentAccounts) > 0 {
		type feeRate struct {
			AccountID string          `json:"accountId"`
			Data      json.RawMessage `json:"data,omitempty"`
			Error     string          `json:"error,omitempty"`
		}
		var rates []feeRate
		for _, acc := range accs.InvestmentAccounts {
			r := feeRate{AccountID: acc.AccountID}
			if data, err := c.FeesAccount(ctx, acc.AccountID); err != nil {
				r.Error = err.Error()
			} else {
				r.Data = data
			}
			rates = append(rates, r)
		}
		b, _ := json.Marshal(rates)
		if err := addJSON("fee_rates", b); err != nil {
			return nil, err
		}
	}

	// Savings-account interest: accrued per account + the current effective
	// rate. Only fetched when the accounts dataset shows savings accounts.
	if sel["accounts"] && len(accs.SavingsAccounts) > 0 {
		type accrued struct {
			AccountID string          `json:"accountId"`
			Data      json.RawMessage `json:"data,omitempty"`
			Error     string          `json:"error,omitempty"`
		}
		out := struct {
			EffectiveInterestRate json.RawMessage `json:"effectiveInterestRate,omitempty"`
			RateError             string          `json:"rateError,omitempty"`
			Accrued               []accrued       `json:"accrued"`
		}{}
		if data, err := c.SavingsInterestRate(ctx); err != nil {
			out.RateError = err.Error()
		} else {
			out.EffectiveInterestRate = data
		}
		for _, acc := range accs.SavingsAccounts {
			a := accrued{AccountID: acc.AccountID}
			if data, err := c.SavingsInterestAccrued(ctx, acc.AccountID); err != nil {
				a.Error = err.Error()
			} else {
				a.Data = data
			}
			out.Accrued = append(out.Accrued, a)
		}
		b, _ := json.Marshal(out)
		if err := addJSON("savings_interest", b); err != nil {
			return nil, err
		}
	}

	if viewerTmpl != "" {
		files["viewer.html"] = buildViewer(viewerTmpl, raws)
	}

	return files, nil
}

// ExportName is the timestamped basename used for the download filename and the
// on-disk folder, e.g. "lysa-export-2006-01-02_150405".
func ExportName() string {
	return "lysa-export-" + time.Now().Format("2006-01-02_150405")
}

// SortedNames returns the file basenames in stable display order.
func SortedNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Zip packs the built files into a zip archive whose entries live under a single
// top-level folder (dirName), so unzipping keeps them together.
func Zip(dirName string, files map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range SortedNames(files) {
		w, err := zw.Create(dirName + "/" + name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteDir writes the built files into outDir/dirName and returns that path.
func WriteDir(outDir, dirName string, files map[string][]byte) (string, error) {
	dir := filepath.Join(outDir, dirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, name := range SortedNames(files) {
		if err := os.WriteFile(filepath.Join(dir, name), files[name], 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// buildViewer bakes the exported datasets into the self-contained viewer HTML so
// it renders offline (file://) with no server.
func buildViewer(tmpl string, raws map[string]json.RawMessage) []byte {
	keys := []string{"accounts", "transactions", "performance", "profile", "advice", "fees", "funds", "tax", "documents"}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if raw, ok := raws[k]; ok && len(raw) > 0 {
			parts = append(parts, fmt.Sprintf("%q:%s", k, string(raw)))
		} else {
			parts = append(parts, fmt.Sprintf("%q:null", k))
		}
	}
	data := "{" + strings.Join(parts, ",") + "}"
	data = strings.ReplaceAll(data, "</", "<\\/") // never break out of the <script> block
	return []byte(strings.Replace(tmpl, "__LYSA_DATA__", data, 1))
}

// --- typed views for CSV flattening ---

type accountsResp struct {
	InvestmentAccounts []struct {
		AccountID string  `json:"accountId"`
		Type      string  `json:"type"`
		Name      string  `json:"name"`
		Created   string  `json:"created"`
		Worth     float64 `json:"worth"`
		Positions map[string]struct {
			Volume float64 `json:"volume"`
			Worth  float64 `json:"worth"`
			Gav    float64 `json:"gav"`
			Type   string  `json:"type"`
		} `json:"positions"`
	} `json:"investmentAccounts"`
	// Savings accounts ride along in the raw accounts.json; only the fields
	// needed for the perf-series start and per-account interest calls are
	// parsed here.
	SavingsAccounts []struct {
		AccountID string `json:"accountId"`
		Name      string `json:"name"`
		Created   string `json:"created"`
	} `json:"savingsAccounts"`
}

func (a *accountsResp) earliestCreated() string {
	best := ""
	upd := func(d string) {
		if len(d) >= 10 {
			d = d[:10]
		}
		if d != "" && (best == "" || d < best) {
			best = d
		}
	}
	for _, acc := range a.InvestmentAccounts {
		upd(acc.Created)
	}
	for _, acc := range a.SavingsAccounts {
		upd(acc.Created)
	}
	return best
}

var positionsHeader = []string{"accountId", "accountName", "accountType", "isin", "volume", "worth", "gav", "instrumentType"}

func (a *accountsResp) positionRows() [][]string {
	var rows [][]string
	for _, acc := range a.InvestmentAccounts {
		isins := make([]string, 0, len(acc.Positions))
		for isin := range acc.Positions {
			isins = append(isins, isin)
		}
		sort.Strings(isins)
		for _, isin := range isins {
			p := acc.Positions[isin]
			rows = append(rows, []string{
				acc.AccountID, acc.Name, acc.Type, isin,
				num(p.Volume), num(p.Worth), num(p.Gav), p.Type,
			})
		}
	}
	return rows
}

type transaction struct {
	Type                string  `json:"type"`
	Booked              string  `json:"booked"`
	AccountID           string  `json:"accountId"`
	Amount              float64 `json:"amount"`
	ISIN                string  `json:"isin"`
	Volume              float64 `json:"volume"`
	ContractNoteID      string  `json:"contractNoteId"`
	DepositChannel      string  `json:"depositChannel"`
	Bank                string  `json:"bank"`
	ExternalBankAccount string  `json:"externalBankAccount"`
}

var txHeader = []string{"booked", "accountId", "type", "isin", "volume", "amount", "depositChannel", "contractNoteId", "bank", "externalBankAccount"}

func txRows(txs []transaction) [][]string {
	sort.Slice(txs, func(i, j int) bool { return txs[i].Booked < txs[j].Booked })
	rows := make([][]string, 0, len(txs))
	for _, t := range txs {
		rows = append(rows, []string{
			t.Booked, t.AccountID, t.Type, t.ISIN, num(t.Volume), num(t.Amount),
			t.DepositChannel, t.ContractNoteID, t.Bank, t.ExternalBankAccount,
		})
	}
	return rows
}

type performanceResp struct {
	Graph []struct {
		Date   string  `json:"date"`
		Worth  float64 `json:"worth"`
		Change float64 `json:"change"`
		Growth float64 `json:"growth"`
	} `json:"graph"`
}

var perfHeader = []string{"date", "worth", "changePct", "growth"}

func (p *performanceResp) rows() [][]string {
	rows := make([][]string, 0, len(p.Graph))
	for _, g := range p.Graph {
		rows = append(rows, []string{g.Date, num(g.Worth), num(g.Change), num(g.Growth)})
	}
	return rows
}

type feePaid struct {
	Date            string  `json:"date"`
	Fee             float64 `json:"fee"`
	FeeExcludingVat float64 `json:"feeExcludingVat"`
	TransactionID   string  `json:"transactionId"`
	AccountID       string  `json:"accountId"`
}

var feesHeader = []string{"date", "fee", "feeExcludingVat", "transactionId", "accountId"}

func feeRows(fees []feePaid) [][]string {
	sort.Slice(fees, func(i, j int) bool { return fees[i].Date < fees[j].Date })
	rows := make([][]string, 0, len(fees))
	for _, f := range fees {
		rows = append(rows, []string{f.Date, num(f.Fee), num(f.FeeExcludingVat), f.TransactionID, f.AccountID})
	}
	return rows
}

type fundsDepot struct {
	DepotID          string `json:"depotId"`
	FundShareClasses []struct {
		Name string `json:"name"`
		ISIN string `json:"isin"`
	} `json:"fundShareClasses"`
}

var fundsHeader = []string{"depotId", "isin", "fund"}

func fundRows(funds []fundsDepot) [][]string {
	var rows [][]string
	for _, d := range funds {
		for _, s := range d.FundShareClasses {
			rows = append(rows, []string{d.DepotID, s.ISIN, s.Name})
		}
	}
	return rows
}

type taxIsk struct {
	AccountID string `json:"accountId"`
	TaxYears  []int  `json:"taxYears"`
}

var taxHeader = []string{"accountId", "taxYear"}

func taxRows(tax []taxIsk) [][]string {
	var rows [][]string
	for _, t := range tax {
		for _, y := range t.TaxYears {
			rows = append(rows, []string{t.AccountID, strconv.Itoa(y)})
		}
	}
	return rows
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
