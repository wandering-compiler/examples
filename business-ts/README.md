# business-ts

One domain whose storage tier is generated and whose **business tier is
written in TypeScript**. Codegen writes no TypeScript: it writes the tier's
manifest (`w17/services/notes-business/w17-business.json`) and the compiled
protos (`descriptors.binpb`), and the [TypeScript business SDK](https://github.com/wandering-compiler/sdk/tree/main/ts/business)
(`@w17/business`) builds the gRPC services from them at boot. The only code
in the project is `business/notes/w17.ts`, run by Node 24 as it is (type
stripping, no build step).

```yaml
# w17/lock.yaml
generated_code:
  business_bundles:
    - domain: notes
      language: typescript
      register: business/notes
```

which is what `w17ctl target business add --domain notes --language typescript
--register business/notes` writes.

It is [`business-python`](../business-python) with the business tier ported
to TypeScript: the same protos, the same suite.

## What it shows

- **Notes** on Postgres (`proto/domains/notes/board/`): a `Note` model, a
  create mutation and a paged list query, all generated.
- **The TypeScript business tier** (`NotesService`, `business/notes/w17.ts`):
  - `CreateNote` writes through the storage mutation and carries
    `(w17.event_emit)`: the SDK publishes `NoteCreated` on NATS after the
    method returns.
  - `CreateNoteInTx` runs `CreateNote` inside a distributed transaction
    (`distx.run`), composed through the served service
    (`registry.self(...)`) so its event waits for the commit. A title
    starting with `rollback:` makes it fail after the write: the transaction
    rolls back, the note is gone and no event is published. On commit, an
    `onCommit` hook records a `committed` mark.
  - `ListNotes` serves a paged REST endpoint: it calls the storage query with
    `paging.forward(ctx)` and copies the query's `w17.Paging`. It knows
    nothing of how storage sorts the page: storage reports where the page
    ends, the SDK relays it, and the gateway builds the next and previous
    cursor from it.
- **A subscriber in the same TypeScript tier** (`NotesEvents.OnNoteCreated`,
  `subscribers.proto`): the generated eventbus runner delivers every
  `NoteCreated` to it; it records a `delivered` mark and publishes
  `NoteDelivered`.
- **REST** for all of it (`proto/domains/notes/rest.proto`), without
  authentication.

## The business package

`business/notes/` is an ordinary Node package:

- `package.json` has `"type": "module"` and no `@w17/business` dependency:
  the image builds the SDK and installs it where `import … from
  "@w17/business"` resolves. List it under `devDependencies` (for the
  editor) if you install it locally, never under `dependencies` — the image
  installs those with `npm ci --omit=dev` and would look for the SDK on the
  npm registry, where it is not. A dependency of your own goes under
  `dependencies` as usual (commit the `package-lock.json`).
- `tsconfig.json` holds the code to what Node can run without compiling:
  `erasableSyntaxOnly` (no `enum`, no `namespace`, no constructor parameter
  properties), `verbatimModuleSyntax` (type-only imports through `import
  type`) and `allowImportingTsExtensions` (relative imports end in `.ts`).
- Messages are protobuf-es v2 messages built from the descriptor set:
  `registry.pool.message("pkg.Name")({ ... })`, camelCase field names
  (`note_id` → `noteId`), 64-bit integers as `bigint`.

## The end-to-end suite

`w17/e2e/notes/tests/rest/` asserts, over REST against the running stack:

| case | what |
|---|---|
| `0010` | a note written by the TypeScript tier; `note.created` and the subscriber's `note.delivered` both arrive on `/w17-events` |
| `0020`, `0042`, `0052`, `0054` | the committed write in a transaction exists, `onCommit` ran once after the commit, and the event held for the commit was delivered |
| `0022`, `0024` | the rolled-back write is refused, no `note.created` arrives, and the batch it wrote into is empty |
| `0030` → `0032` → `0034` | page 1, page 2 and back to page 1 of `ListNotes`, through the TypeScript tier |
| `0040`, `0050`, `0060` | the note is stored, the subscriber marked it exactly once, the storage query pages the same |

## Running it

```sh
w17ctl login            # once
w17ctl codegen          # generate the services, the manifest and the descriptor set
w17ctl test             # bring the stack up, run the e2e suite, tear it down
```

The business image builds the TypeScript business SDK from the sdk
repository at the tag `ts/<sdk_version>` (the lock's `sdk_version`), packs it
and installs it. Generated against a wandering-compiler checkout
(`W17_WANDERING_COMPILER_PATH`), it builds the checkout's `sdk/ts/business`
instead.
