package lysa

import (
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
	{"accounts", "Accounts & holdings", "Current positions per account (ISIN, volume, worth, average cost)"},
	{"transactions", "Transaction history", "Every transaction (buys, sells, deposits, fees, switches) with ISINs"},
	{"performance", "Performance", "Daily portfolio value series + total return"},
	{"profile", "Profile", "Legal entity / KYC (name, personal number, email)"},
	{"advice", "Advice & risk", "Per-account advised vs taken risk and suitability declaration"},
	{"fees", "Fees paid", "Every management fee charged (date, amount, VAT)"},
	{"funds", "Fund catalog", "The funds you hold — ISIN, name, share class"},
	{"tax", "Tax years", "Available ISK tax (deklaration) years per account"},
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

// Export fetches the selected datasets and writes them into outDir as raw JSON
// (always) plus flattened CSV (for accounts/transactions/performance). It
// returns the basenames of the files written.
func (c *Client) Export(ctx context.Context, outDir string, selected []string, viewerTmpl string) (string, []string, error) {
	if !c.Authed() {
		return "", nil, fmt.Errorf("not authenticated")
	}
	sel := map[string]bool{}
	for _, s := range selected {
		if validKey(s) {
			sel[s] = true
		}
	}
	if len(sel) == 0 {
		return "", nil, fmt.Errorf("no valid datasets selected")
	}

	// Each export lands in its own timestamped folder.
	dir := filepath.Join(outDir, "lysa-export-"+time.Now().Format("2006-01-02_150405"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, err
	}

	today := time.Now().UTC().Format("2006-01-02")
	const earliest = "2000-01-01"

	raws := map[string]json.RawMessage{}
	var files []string
	writeJSON := func(name string, raw json.RawMessage) error {
		raws[name] = raw
		var pretty any
		if err := json.Unmarshal(raw, &pretty); err != nil {
			return err
		}
		b, _ := json.MarshalIndent(pretty, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, name+".json"), b, 0o644); err != nil {
			return err
		}
		files = append(files, name+".json")
		return nil
	}
	writeCSV := func(name string, header []string, rows [][]string) error {
		f, err := os.Create(filepath.Join(dir, name+".csv"))
		if err != nil {
			return err
		}
		defer f.Close()
		w := csv.NewWriter(f)
		_ = w.Write(header)
		_ = w.WriteAll(rows)
		w.Flush()
		if err := w.Error(); err != nil {
			return err
		}
		files = append(files, name+".csv")
		return nil
	}

	// accounts/all is needed both as a dataset and to derive the performance
	// start date, so fetch it once if either is selected.
	var accounts *accountsResp
	if sel["accounts"] || sel["performance"] {
		raw, err := c.AccountsAll(ctx)
		if err != nil {
			return dir, files, err
		}
		var a accountsResp
		if err := json.Unmarshal(raw, &a); err != nil {
			return dir, files, err
		}
		accounts = &a
		if sel["accounts"] {
			if err := writeJSON("accounts", raw); err != nil {
				return dir, files, err
			}
			if err := writeCSV("positions", positionsHeader, a.positionRows()); err != nil {
				return dir, files, err
			}
		}
	}

	if sel["transactions"] {
		raw, err := c.Transactions(ctx, earliest, today)
		if err != nil {
			return dir, files, err
		}
		if err := writeJSON("transactions", raw); err != nil {
			return dir, files, err
		}
		var txs []transaction
		if err := json.Unmarshal(raw, &txs); err != nil {
			return dir, files, err
		}
		if err := writeCSV("transactions", txHeader, txRows(txs)); err != nil {
			return dir, files, err
		}
	}

	if sel["performance"] {
		start := earliest
		if accounts != nil {
			if e := accounts.earliestCreated(); e != "" {
				start = e
			}
		}
		raw, err := c.Performance(ctx, start, today)
		if err != nil {
			return dir, files, err
		}
		if err := writeJSON("performance", raw); err != nil {
			return dir, files, err
		}
		var p performanceResp
		if err := json.Unmarshal(raw, &p); err != nil {
			return dir, files, err
		}
		if err := writeCSV("performance", perfHeader, p.rows()); err != nil {
			return dir, files, err
		}
	}

	if sel["profile"] {
		raw, err := c.LegalEntity(ctx)
		if err != nil {
			return dir, files, err
		}
		if err := writeJSON("profile", raw); err != nil {
			return dir, files, err
		}
	}

	if sel["advice"] {
		raw, err := c.Advice(ctx)
		if err != nil {
			return dir, files, err
		}
		if err := writeJSON("advice", raw); err != nil {
			return dir, files, err
		}
	}

	if sel["fees"] {
		raw, err := c.FeesPaid(ctx)
		if err != nil {
			return dir, files, err
		}
		if err := writeJSON("fees", raw); err != nil {
			return dir, files, err
		}
		var fees []feePaid
		if err := json.Unmarshal(raw, &fees); err != nil {
			return dir, files, err
		}
		if err := writeCSV("fees", feesHeader, feeRows(fees)); err != nil {
			return dir, files, err
		}
	}

	if sel["funds"] {
		raw, err := c.FundsSummary(ctx)
		if err != nil {
			return dir, files, err
		}
		if err := writeJSON("funds", raw); err != nil {
			return dir, files, err
		}
		var funds []fundsDepot
		if err := json.Unmarshal(raw, &funds); err != nil {
			return dir, files, err
		}
		if err := writeCSV("funds", fundsHeader, fundRows(funds)); err != nil {
			return dir, files, err
		}
	}

	if sel["tax"] {
		raw, err := c.TaxIskYears(ctx)
		if err != nil {
			return dir, files, err
		}
		if err := writeJSON("tax", raw); err != nil {
			return dir, files, err
		}
		var tax []taxIsk
		if err := json.Unmarshal(raw, &tax); err != nil {
			return dir, files, err
		}
		if err := writeCSV("tax_years", taxHeader, taxRows(tax)); err != nil {
			return dir, files, err
		}
	}

	if sel["documents"] {
		raw, err := c.Documents(ctx)
		if err != nil {
			return dir, files, err
		}
		if err := writeJSON("documents", raw); err != nil {
			return dir, files, err
		}
	}

	if viewerTmpl != "" {
		if err := writeViewer(dir, viewerTmpl, raws); err != nil {
			return dir, files, err
		}
		files = append(files, "viewer.html")
	}

	sort.Strings(files)
	return dir, files, nil
}

// writeViewer bakes the exported datasets into the self-contained viewer HTML so
// it renders offline (file://) with no server.
func writeViewer(dir, tmpl string, raws map[string]json.RawMessage) error {
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
	html := strings.Replace(tmpl, "__LYSA_DATA__", data, 1)
	return os.WriteFile(filepath.Join(dir, "viewer.html"), []byte(html), 0o644)
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
}

func (a *accountsResp) earliestCreated() string {
	best := ""
	for _, acc := range a.InvestmentAccounts {
		d := acc.Created
		if len(d) >= 10 {
			d = d[:10]
		}
		if d != "" && (best == "" || d < best) {
			best = d
		}
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
