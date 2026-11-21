# 3DS authentication is keyed by the card number's fingerprint

A card number can be tokenized repeatedly, producing several Card records. Issuers authenticate the
instrument (the number), not our record of it, so authentication state is stored against the
fingerprint (`base64(sha256(digits))`, the same shape Omise exposes on every card) rather than
against the `card_` identifier. A new Card whose fingerprint is already authenticated never triggers
a challenge, and `card.three_ds_authenticated_at` is populated for it.

Keying by `card_` id would isolate tests perfectly but would model something that does not exist: it
would mean the same physical card needs a fresh challenge every time the merchant re-vaults it.

## Consequences

- Authentication survives deleting and re-adding the same card, and survives creating a second
  customer for the same payer.
- Tests that share a database can see authentication bleed across customers that use the same test
  card number. The embedded server avoids this by using a fresh database per test, and
  `POST /_fake/reset` clears it.
- The fingerprint is derived at tokenization and never recomputed from a number stored on disk; the
  fake keeps no PAN.
