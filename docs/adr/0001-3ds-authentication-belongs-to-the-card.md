# 3DS Authentication belongs to the Card, not the Charge

Real Omise applies 3-D Secure at the account level and disables Charge Schedules for 3DS-enabled
accounts; it records nothing about a Card having been authenticated, because it never charges a
stored Card without a fresh challenge. Our product requirement is the opposite — the first charge
authenticates the Card, and every later Merchant-Initiated Charge on that Card must succeed without
a challenge — so we model **3DS Authentication as a property of the Card**
(`three_ds_authenticated_at`), and a Charge whose Card is already authenticated never produces an
`authorize_uri`.

This is a deliberate deviation from Omise, not a gap to be "fixed" later: a future reader will see
a `card.three_ds_authenticated_at` field that does not exist in the Omise API and should not remove
it in the name of wire fidelity.

## Consequences

- A Charge on a Card that has never been authenticated comes back `pending` with an
  `authorize_uri`, even when the merchant expected an automatic success.
- Nothing about 3DS is inferred from the account, the Customer or the request parameters; the Card
  is the only source of truth.
