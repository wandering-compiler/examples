# bookshop

A catalog of authors and books.

- **Models** (`proto/domains/catalog/books/types/models.proto`): `Author`, and
  `Book` with a foreign key to its author, a unique ISBN validated by a pattern,
  and a price that may not be negative. w17 turns them into tables, migrations
  and validation — there is no hand-written SQL or check anywhere.
- **Queries and mutations** (`queries/`, `mutations/`): each RPC is one DQL
  statement; w17 generates the storage service that runs it.
- **REST API** (`proto/domains/catalog/rest.proto`): browsing (`GET /books`,
  `GET /books/{id}`) is public, everything that writes needs a signed-in
  account.
- **Accounts** come from the `auth` plugin, activated in
  `proto/domains/catalog/w17.proto`: sign-up, sign-in, bearer tokens. Its
  routes are mounted on the same API (`include: [ { plugin: "auth" } ]`).
- **End-to-end tests** (`w17/e2e/catalog/`): sign-up and sign-in, creating and
  changing books, the refusals (a duplicate ISBN, an invalid one), deleting and
  reading back, and that browsing works without a token.

```sh
w17ctl codegen
w17ctl test
```
