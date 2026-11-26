# fake-omise

A stand-in for the [Omise](https://docs.omise.co) payment API for local development and tests.
It speaks enough of the real API — same payloads, same identifiers, same error envelope — that the
official SDKs work against it, and it models the one thing Omise's sandbox makes awkward to test:
**a card that authenticates with 3-D Secure once, then gets charged again and again without a
challenge.**

Go, one binary, SQLite. No account, no dashboard, no network.

## Why

Omise disables Charge Schedules for 3-D Secure-enabled accounts and has no concept of a card that is
"already authenticated", so there is no way to walk the real card-on-file story end to end:

```
3DS + 0 THB validation charge  →  card authenticated  →  merchant-initiated charges, no 3DS
```

This fake implements that story, keeps the Omise wire format, and lets you decide *when* a challenge
happens so failure paths are testable.

## Quick start

```bash
go run ./cmd/fake-omise -addr :8080 -db ./fake-omise.db

# tokenize (any test key works)
TOKEN=$(curl -s -u skey_test_4xs8breq3htbkj03d2x: http://localhost:8080/tokens \
  -d 'card[number]=4242424242424242' \
  -d 'card[expiration_month]=12' \
  -d 'card[expiration_year]=2030' \
  -d 'card[security_code]=123' | jq -r .id)

# store the card on a customer
CUSTOMER=$(curl -s -u skey_test_4xs8breq3htbkj03d2x: http://localhost:8080/customers \
  -d "card=$TOKEN" -d 'email=somchai@example.com' | jq -r .id)

# 0 THB validation charge — returns pending + authorize_uri
curl -s -u skey_test_4xs8breq3htbkj03d2x: http://localhost:8080/charges \
  -d amount=0 -d currency=THB -d "customer=$CUSTOMER" \
  -d 'return_uri=https://merchant.example/orders/1/complete'

# later charges just work — no 3DS, no return_uri
curl -s -u skey_test_4xs8breq3htbkj03d2x: http://localhost:8080/charges \
  -d amount=4900 -d currency=THB -d "customer=$CUSTOMER"
```

Open the `authorize_uri` in a browser to play the cardholder, or finish it headlessly:

```bash
curl -s -u skey_test_4xs8breq3htbkj03d2x: -X POST \
  http://localhost:8080/_fake/charges/$CHARGE/authorize -d outcome=success
```

### Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `-addr` | `:8080` | listen address |
| `-db` | `fake-omise.db` | SQLite file (`:memory:` works) |
| `-3ds` | `always` | `always`, `zero_amount` or `off` — when a challenge is required |
| `-webhook` | none | URL that receives event webhooks |
| `-base-url` | request host | origin used to build `authorize_uri` |

## Docker

```bash
docker build -t fake-omise .
docker run --rm -p 8080:8080 -v fake-omise-data:/data fake-omise

# any flag can be passed through
docker run --rm -p 8080:8080 fake-omise -3ds zero_amount -webhook http://host.docker.internal:9000/hook
```

The image is distroless, runs as non-root, and keeps its SQLite database in `/data` (mount a volume to
survive restarts). CI builds and smoke-tests the image on every push; tagged releases publish
multi-arch images to GHCR:

```bash
docker run --rm -p 8080:8080 ghcr.io/akaporn-katip/fake-omise:v0.1.0
```

## Embedded in Go tests

```go
srv := fakeomise.NewTestServer(t, fakeomise.Options{}) // fresh SQLite per test, closed automatically
defer srv.Close()

// point your client at srv.URL
```

## Endpoints

| Method | Path | Notes |
| --- | --- | --- |
| POST | `/tokens` | single-use card tokens |
| GET/POST | `/customers`, `/customers/{id}` | create, list, retrieve, update (incl. `default_card`) |
| DELETE | `/customers/{id}` | soft delete |
| GET/POST | `/customers/{id}/cards` | list, add from token |
| GET/DELETE | `/customers/{id}/cards/{card}` | retrieve, delete (promotes a new default) |
| POST/GET | `/charges`, `/charges/{id}` | create, list, retrieve |
| POST | `/charges/{id}/capture` | capture an authorized charge, optionally partially |
| GET/POST | `/payments/{id}/authorize` | the Authorization Page (no API key needed) |
| GET | `/events`, `/events/{id}` | event log with webhook deliveries |
| POST | `/_fake/charges/{id}/authorize` | headless 3DS completion |
| POST | `/_fake/reset` | wipe every object |

Authentication is Omise-style Basic auth: any `skey_test_*` or `pkey_test_*` works, live keys are
rejected.

## How the 3DS flow behaves

1. `POST /charges` for a card whose fingerprint has not been authenticated →
   `status: "pending"`, `authorize_uri`, `expires_at` (one hour).
2. The cardholder opens `authorize_uri`, enters any OTP, and chooses **Authenticate** or **Decline**.
3. The fake marks the charge, records the fingerprint as authenticated, and redirects to `return_uri`.
4. `GET /charges/{id}` returns the truth — the redirect itself never decides anything.

A challenge is *not* issued when the card's fingerprint is already authenticated, which is what makes
merchant-initiated charges silent.

### Forcing an outcome

```bash
curl ... -H 'X-Fake-3DS: off'          # skip 3DS for this request
curl ... -H 'X-Fake-3DS: challenge'    # require it, even for a known card
curl ... -H 'X-Fake-3DS: frictionless' # succeed without a challenge and mark the card authenticated
```

### Test cards

The documented Omise test numbers behave the same way here (`https://docs.omise.co/api-testing`):

| Card | Result |
| --- | --- |
| `4242 4242 4242 4242`, `4111 1111 1111 1111`, `5555 5555 5555 4444`, … | successful charge, 3DS challenge |
| `4111 1111 1114 0011` | `insufficient_fund` at creation |
| `4111 1111 1113 0012` | `stolen_or_lost_card` |
| `4111 1111 1112 0013` | `failed_processing` |
| `4111 1111 1111 0014` | `payment_rejected` |
| `4111 1111 1119 0016` | `failed_fraud_check` |
| `4111 1111 1118 0017` | `invalid_account_number` |
| `4111 1111 1115 0002` | 3DS *enrollment* failure — the charge fails at creation |
| `4111 1111 1114 0003` | 3DS *validation* failure — the challenge always fails |

Any other 12–19 digit number is treated as a successful card. A missing `return_uri` on a charge that
needs 3DS fails with `payment_rejected`, as Omise does.

## Using the real SDKs

`omise-go` hard-codes its endpoints, so rewrite the host in a transport:

```go
client, _ := omise.NewClient("pkey_test_x", "skey_test_x")
client.Transport = fakeTransport{base: "http://localhost:8080"}
```

```go
type fakeTransport struct{ base string }

func (t fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host == "api.omise.co" || r.URL.Host == "vault.omise.co" {
		r.URL.Scheme, r.URL.Host = "http", strings.TrimPrefix(t.base, "http://")
	}
	return http.DefaultTransport.RoundTrip(r)
}
```

When you proxy this way the request keeps `Host: api.omise.co`, so start the server with
`-base-url http://localhost:8080` to keep `authorize_uri` pointing at the fake. This exact setup is
verified against `github.com/omise/omise-go` v1.7.2.

## Webhooks

Events are always persisted and readable at `GET /events`. When `-webhook` is set, each event is
POSTed as JSON with up to three attempts (200 ms, then 600 ms backoff) and every attempt is recorded
in the event's `webhook_deliveries`. There is no signature header — verify nothing.

Emitted keys: `charge.create`, `charge.complete`, `charge.capture`, `customer.create`,
`customer.update`, `customer.destroy`, `card.create`, `card.destroy`, `token.create`.

## Deliberate deviations from Omise

| Behaviour | Omise | fake-omise |
| --- | --- | --- |
| 3DS scope | account-level, applies to every charge | per card fingerprint; repeat charges are silent |
| `card.three_ds_authenticated_at` | does not exist | exposed so tests can see why a challenge was skipped |
| Charge Schedules | exists, disabled for 3DS accounts | not implemented — recurring is the merchant's job |
| 0 THB charges | needs the account configured for 3DS validation | always allowed |
| Fees | real pricing per account | fixed 3.65 % + 7 % VAT |
| Refunds, transfers, sources, links, disputes, search | exist | not implemented |
| Webhook signatures | `Omise-Signature` | none |

Reasoning lives in [`docs/adr/`](./docs/adr) and the vocabulary in [`CONTEXT.md`](./CONTEXT.md).

## Development

```bash
go test ./...
go vet ./...
```

`internal/api/api_test.go` covers the 3DS-then-silent flow, single-use tokens, declines, validation
and enrollment failures, capture, webhooks, card management and pagination.
