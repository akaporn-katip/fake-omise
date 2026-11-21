# Fake Omise

A stand-in for the Omise payment API used to exercise merchant integrations — especially the
3-D Secure and card-on-file payment flows — without touching the real Omise account.

## Language

**Charge**:
A single attempt to move a fixed amount of money from a Card or Customer to the merchant.
_Avoid_: Payment, transaction, purchase

**Charge Status**:
Where a Charge sits in its lifecycle: `pending` while it awaits authorization, then `successful`,
`failed` or `expired`.
_Avoid_: State, phase

**Card**:
A payment instrument stored against a Customer (or attached to a single Charge), identified by a
`card_` identifier. A Card is what gets authenticated; it is not the same thing as the Token that
created it.
_Avoid_: Instrument, payment method, source

**Customer**:
The payer on whose behalf Cards are stored so they can be charged again later.
_Avoid_: User, account, client

**Token**:
A single-use representation of card details, produced by the tokenization endpoint and consumed
when the Card it represents is stored or charged. A second use is an error, not a second charge.
_Avoid_: Card token, nonce

**Fingerprint**:
A stable hash of a Card's number, shared by every Card created from that number. It is the identity
of the payment instrument, and the unit that 3DS Authentication is recorded against.
_Avoid_: Hash, card hash

**3DS Authentication**:
The act of the cardholder proving possession of a Card through the issuer's 3-D Secure challenge.
It is a property of the Card's Fingerprint and happens once per instrument.
_Avoid_: Verification, OTP step, 3DS charge

**Card-on-file**:
A Card that has been stored against a Customer and may be charged again without the cardholder
re-entering details.
_Avoid_: Saved card, stored card, vaulted card

**Merchant-Initiated Charge**:
A Charge the merchant creates on a Card-on-file without the cardholder present, and therefore
without a fresh 3DS Authentication.
_Avoid_: Recurring charge, auto-charge, off-session charge, MIT

**Authorization Page**:
The page served at a pending Charge's `authorize_uri` that stands in for the issuer's 3DS
challenge, where a cardholder approves or declines the authentication.
_Avoid_: 3DS page, bank page, redirect page
