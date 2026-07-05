# lysa-export

A small self-contained tool to export **your own** [Lysa](https://lysa.se)
account data. You run it as a Docker container; it serves a local web page with
a **BankID QR code**, and once you've logged in you tick which datasets you want.
It writes them to disk as **JSON + CSV** and then exits.

It talks to Lysa's undocumented internal API (`api.lysa.se`) — the same one the
web app uses — driving Lysa's own BankID login and reusing the session token.
There is **no official Lysa API**; this is unofficial and unsupported.

## What you can export

| Dataset | Contents |
|---|---|
| Accounts & holdings | Current positions per account — ISIN, volume, worth, average cost (GAV) |
| Transaction history | Every transaction (buy/sell/deposit/withdrawal/fee/switch) with ISINs |
| Performance | Daily portfolio value series + total return |
| Profile | Legal entity / KYC (name, personal number, email) |
| Advice & risk | Per-account advised vs taken risk and suitability declaration |

Each selected dataset is written as `<name>.json`; accounts, transactions and
performance additionally get a flattened `<name>.csv` (`positions.csv`,
`transactions.csv`, `performance.csv`).

## Usage

```bash
docker build -t lysa-export .
docker run --rm -p 8080:8080 -v "$PWD/lysa-export-out:/out" lysa-export
```

Then open <http://localhost:8080>, scan the QR with your BankID app, choose your
datasets, and click **Export**. Files land in `./lysa-export-out`; the container
exits on its own when done.

### Run from source (no Docker)

```bash
OUT_DIR=./lysa-export-out go run .
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
4. The selected data endpoints are fetched and written out; the process exits.

The session token lasts ~30 minutes — far longer than the couple of minutes the
whole export takes — so there's no session-refresh machinery. If you log in and
then walk away for half an hour before exporting, just restart and log in again.

## Configuration

| Env var | Default | Purpose |
|---|---|---|
| `PORT` | `8080` | Web UI port |
| `OUT_DIR` | `/out` | Where export files are written |
| `LYSA_BUILD_HASH` | *(baked-in)* | The SPA `hash` sent on login calls. If login starts failing after a Lysa frontend deploy, copy a fresh `hash=` value from any `api.lysa.se` request URL and set it here. |

## Caveats

- **Personal, read-only, your own account.** Undocumented and unsupported;
  endpoints and fields can change without notice.
- Automating the BankID credential itself is **not** attempted (and shouldn't be):
  a human scan is required to start each session. This tool only automates
  everything *after* that login.
- Keep the container local — the web UI has no auth of its own and briefly holds
  a live session token in memory.
