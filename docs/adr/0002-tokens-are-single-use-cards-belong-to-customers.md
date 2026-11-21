# Tokens are single-use and cards belong to customers

Omise tokens (`tokn_`) are single-use: a second charge with the same token returns
`400 used_token`. That makes a token useless for anything recurring, so charging a stored card
requires an owner — a Customer — whose card the merchant references on later charges. We implement
both rules faithfully rather than relaxing them, because a fake that accepts token reuse lets
merchant code pass here and fail in production.

The alternative — letting a `card_` identifier be charged without a customer — would remove the
`POST /customers` step entirely and simplify the fake. We rejected it: it contradicts Omise's own
contract ("if a card identifier is passed, you must also pass the identifier of the customer who
owns the card") and it breaks the link between a card and the customer whose recurring payments it
serves.

## Consequences

- There is no way to see recurring behaviour from tokens alone; every test that wants a second
  charge on the same instrument must create a customer first.
- Charging someone else's card id is a 404, not a 403, matching Omise's object-not-found behaviour.
- Deleting the default card promotes another card so that a bare `customer` charge keeps working.
