# lysa-export

A small self-contained tool to export **your own** [Lysa](https://lysa.se)
account data. You run it as a Docker container; it serves a local web page with
a **BankID QR code**, and once you've logged in you tick which datasets you want.
It bundles them (**JSON + CSV**, plus a self-contained offline HTML viewer) into a
**zip your browser downloads**, then exits. Optionally it also writes a copy to
disk (see `OUT_DIR`).

It talks to Lysa's undocumented internal API (`api.lysa.se`) — the same one the
web app uses — driving Lysa's own BankID login and reusing the session token.
There is **no official Lysa API**; this is unofficial and unsupported.

## What you can export

| Dataset | Contents |
|---|---|
| Accounts & holdings | Current positions per account — ISIN, volume, worth, average cost (GAV) — incl. savings accounts (+ accrued interest & current rate, `savings_interest.json`) |
| Transaction history | Every transaction (buy/sell/deposit/withdrawal/fee/switch) with ISINs |
| Performance | Daily portfolio value series + total return |
| Profile | Legal entity / KYC (name, personal number, email) |
| Advice & risk | Per-account advised vs taken risk and suitability declaration |
| Fees paid | Every management fee charged (date, amount, VAT) + current fee rates per account (`fee_rates.json`) |
| Fund catalog | The funds you hold — ISIN, name, share class + full look-through holdings (`fund_holdings.json`) |
| Tax years | Available ISK tax (deklaration) years per account + per-year detail (`tax_isk.json`) |
| Documents | Register of your statements & documents |

Each selected dataset is written as `<name>.json`; accounts, transactions,
performance, fees, funds and tax additionally get a flattened `<name>.csv`
(`positions.csv`, `transactions.csv`, `performance.csv`, `fees.csv`,
`funds.csv`, `tax_years.csv`).

## Usage

```bash
docker build -t lysa-export .
docker run --rm -p 8080:8080 lysa-export
```

Then open <http://localhost:8080>, scan the QR with your BankID app, choose your
datasets, and click **Export & download**. Your browser downloads
`lysa-export-<timestamp>.zip`; the container exits once the download completes.

To **also keep a copy on disk**, set `OUT_DIR` and mount a volume there:

```bash
docker run --rm -p 8080:8080 -e OUT_DIR=/out -v "$PWD/lysa-export-out:/out" lysa-export
```

### Run from source (no Docker)

```bash
go run .                    # download only
OUT_DIR=./lysa-export-out go run .   # also write a copy to disk
```

## How it works

1. `POST /bankid/login` starts a BankID order (returns an `orderRef`).
2. `GET /bankid/login/qr/{orderRef}` returns the animated-QR payload each second;
   the backend renders it to a PNG the page refreshes. (Lysa computes the BankID
   HMAC server-side — this tool does no BankID cryptography and is **not** a
   BankID relying party; it only relays Lysa's own login.)
3. `GET /bankid/login/{orderRef}` is polled until `status: complete`, which sets
   the `lysa-token` cookie. The token is kept **in memory only** and never written
   to disk or logged.
4. The selected data endpoints are fetched and bundled — in memory — into a zip
   (JSON + CSV + a self-contained `viewer.html`) that the browser downloads. If
   `OUT_DIR` is set, a copy is also written there. The container exits once the
   browser confirms it has the download.

The session token lasts ~30 minutes, so there's no session-refresh machinery. If
you log in and then walk away for half an hour before exporting, just restart and
log in again.

## Configuration

| Env var | Default | Purpose |
|---|---|---|
| `PORT` | `8080` | Web UI port |
| `OUT_DIR` | *(unset)* | If set, also write a copy of the export to this dir (mount a volume there). Unset = browser download only. |

## Caveats

- **Personal, read-only, your own account.** Undocumented and unsupported;
  endpoints and fields can change without notice.
- Automating the BankID credential itself is **not** attempted (and shouldn't be):
  a human scan is required to start each session. This tool only automates
  everything *after* that login.
- Keep the container local — the web UI has no auth of its own and briefly holds
  a live session token in memory.
