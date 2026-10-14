# single-binary

One domain whose five components (gateway, storage, business, admin and
eventbus) are built into one binary, `app-server`, instead of one service
each. The tiers call each other in-process.

```yaml
# w17/lock.yaml
generated_code:
  binaries:
    - domain: app
      components: [admin, business, eventbus, gateway, storage]
```

## What it shows

- **Notes** on Postgres (`proto/domains/app/example/`), behind REST.
- **A business tier** (`NotesFacade`, hand-written under `srcgo`) that runs
  a rule, then calls the storage tier. Two of its methods call another
  facade method through the served server, one of them inside a
  transaction that may be rolled back.
- **Events on NATS:** `NoteCreated` from the storage tier and `NoteChecked`
  from the business tier, with subscribers in the same domain
  (`subscribers.proto`). One handler reacts to the `auth` plugin's
  `UserCreated`.
- **Uploads:** the gateway stores a multipart file and the business tier
  reads it back from the same bucket.
- **An admin** for the notes, behind a login of the `auth` plugin. The
  admin keeps its own listener (:9090) inside the binary, beside the
  gateway's (:8080).

The generated `compose.w17.yaml` declares `app-server` and the
infrastructure, and no service for any component folded into it.

## Tests

- `w17/e2e/app/` is the end-to-end suite, over REST and the admin, from the
  rows in `fixtures/app/test.json` (notes, and the admin account it signs in
  with). `compose.single-binary.yaml` adds the step that seeds them, with
  `app-server fixtures apply`, before the binary starts.
- `livetest/` checks what the generated suite cannot: that the stack has no
  standalone service for a folded component, that `app-server`'s
  Dockerfile builds the admin's console (`vite build`), that each event is
  delivered exactly once, that the business tier's event is published, and
  that the business tier reads a file the gateway wrote.

## Run it

```sh
w17ctl codegen
livetest/run.sh      # the e2e suite, then the live suite, on one stack
```
