# reference

The example that exercises the most of w17 at once: two domains, several
databases, every API surface, events, and a hand-written business tier. Where
[bookshop](../bookshop) is the place to start reading, this one is the place
to look up how a feature is declared and what the generated code does with it.
Its end-to-end suite runs on every change to the SDK, the plugins and these
examples.

## Domains

- **app** (`proto/domains/app/`): users, tasks, projects, tags, attachments
  and comments on Postgres (the `app-postgres` connection), plus sessions,
  tokens and device profiles in Redis (`app-redis`) — two connections in one
  domain. It activates the `auth` plugin for accounts and sign-in, and the
  small `auditsink` plugin that lives in this example (`proto/plugins/`).
- **billing** (`proto/domains/billing/`): accounts, plans, subscriptions,
  usage, invoices, payments and a ledger on its own Postgres
  (`billing-postgres`). It activates `auditsink` again under another name, so
  one plugin serves two domains.

## What it shows

- **Queries and mutations** in DQL: joins, prefetch, aggregates with
  `GROUP BY`, correlated subqueries, `FOR UPDATE` locks, write guards, and
  transactions across several statements.
- **Surfaces:** a REST API (`rest.proto`) with cursor paging, multipart
  uploads, server-sent events and WebSocket streams; an MCP server
  (`mcp.proto`); and an admin (`admin.proto`) with an ACL.
- **Authentication:** the `auth` plugin's accounts and roles, and a
  hand-written capability-link method (a share token in the URL) that grants
  read access to one note.
- **Events:** lifecycle events on a durable NATS channel, consumed by
  subscribers in the same domain (`subscribers.proto`).
- **Request data:** every request to the REST API, the MCP server and the
  admin is recorded on the domain's request stream, and an error report
  carries the request that caused it — both without the fields marked
  `SECRET` or `sensitive`. A client fault report (`ReportClientFault`) is
  recorded before it is answered, with its response, and a consumer of the
  stream keeps an audit trail in Redis (the `requestaudit` package under
  `srcgo`). The live suite reads all three back.
- **Validation** from field annotations, including a hand-written validator
  that codegen calls (the `validators` package of the app domain under
  `srcgo`).
- **Languages:** catalogs in `w17/languages/` (English and Czech) and
  per-locale number formats.

## Hand-written code

The app domain's code under `srcgo` is written by hand: the business
server (`cmd/grpcapi-business`) hosts the facade services and reaches the
generated storage tier over gRPC, using the clients codegen writes into
`srcgo/gen/`. `compose.reference.yaml` adds it to the generated stack and
points the gateway at it. Everything else under `w17/services/` and
`srcgo/gen/` is generated and not committed.

## Run it

```sh
w17ctl codegen
w17ctl test
```

`w17ctl test` brings the stack up from `compose.yaml` and runs the suite in
`w17/e2e/app/` against the REST, MCP and admin surfaces. Seed rows for the
suite are in `w17/e2e/app/fixtures/` and `fixtures/app/`.
