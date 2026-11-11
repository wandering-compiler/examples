# business-rs

One domain whose storage tier is generated and whose **business tier is
written in Rust**. Codegen writes no Rust: it writes the tier's manifest
(`w17/services/notes-business/w17-business.json`) and the compiled protos
(`descriptors.binpb`). The crate `business/notes/` compiles the descriptor
set into tonic traits in its `build.rs` (`w17-business-build`, no `protoc`),
implements them, and the [Rust business SDK](https://github.com/wandering-compiler/sdk/tree/main/rs/w17-business)
(`w17-business`) serves them. A handler is checked against its proto at
compile time.

```yaml
# w17/lock.yaml
generated_code:
  business_bundles:
    - domain: notes
      language: rust
      register: business/notes
```

which is what `w17ctl target business add --domain notes --language rust
--register business/notes` writes. The register is the crate's directory;
its last segment, `notes`, is the crate's package and binary name.

It is [`business-python`](../business-python) with the business tier ported
to Rust: the same protos, the same suite.

## What it shows

- **Notes** on Postgres (`proto/domains/notes/board/`): a `Note` model, a
  create mutation and a paged list query, all generated.
- **The Rust business tier** (`NotesService`, `business/notes/src/main.rs`):
  - `create_note` writes through the storage mutation and carries
    `(w17.event_emit)`: the SDK publishes `NoteCreated` on NATS after the
    method returns.
  - `create_note_in_tx` runs `CreateNote` inside a distributed transaction
    (`distx::run`), composed through the served service
    (`registry.self_channel()`) so its event waits for the commit. A title
    starting with `rollback:` makes it fail after the write: the transaction
    rolls back, the note is gone and no event is published. On commit, an
    `on_commit` hook records a `committed` mark.
  - `list_notes` serves a paged REST endpoint: it calls the storage query
    with `paging::forward(&ctx)` and copies the query's `w17.Paging`. It
    knows nothing of how storage sorts the page: storage reports where the
    page ends, the SDK relays it, and the gateway builds the next and
    previous cursor from it.
- **A subscriber in the same Rust tier** (`NotesEvents::on_note_created`,
  `subscribers.proto`): the generated eventbus runner delivers every
  `NoteCreated` to it; it records a `delivered` mark and publishes
  `NoteDelivered`.
- **The business companion** (`w17/services/notes-business-companion/`,
  generated): what a Go tier runs in-process — the egress clients of
  `proto/clients/` and the upload file stores — served over gRPC to the Rust
  tier by the same Go code. Its own container here; folded into the domain's
  binary when the domain is composed.
  - `quote` calls the third-party Quotes API through the generated egress
    client `proto/clients/quotes` (`clients.egress("quotes")`, a channel for
    the generated `QuotesApiClient`). The companion makes the HTTP request
    with the credential from `W17_CLIENT_QUOTES_DSN` and codes the API's
    errors for the caller: an unknown quote is `NOT_FOUND`. `quotestub/`
    stands in for the API in `compose.yaml`.
  - `export_note` renders a note as a text file, puts it on its own
    connection `notes-exports` (`clients.store("notes-exports")`, the
    placement of the `writer: BUSINESS` field `NoteExport.file`) and
    records the handle through a storage mutation. The gateway serves it
    back through the `/files` download route. Gateway and companion are
    separate containers, so the generated composes mount one volume,
    `notes-uploads-notes-exports`, into both: one connection is one store.
- **REST** for all of it (`proto/domains/notes/rest.proto`), without
  authentication.

## The business crate

`business/notes/` is an ordinary binary crate:

- `Cargo.toml` depends on `w17-business` and, as a build dependency,
  `w17-business-build`, both from the sdk repository at the tag
  `rs/<sdk_version>` (the lock's `sdk_version`): there is no package
  registry. `tonic` and `prost` are its own dependencies, at the majors the
  SDK is built on (0.14), because the generated code names them by path. Its
  empty `[workspace]` keeps it out of any workspace above it.
- `Cargo.lock` is committed: the image builds `--locked`, and refuses a lock
  that takes the SDK from another tag than the project's `sdk_version`. To
  move the SDK, change the `tag` of both crates, run `cargo update -p
  w17-business -p w17-business-build`, and commit the lock.
- `build.rs` is `w17_business_build::compile()`: it finds the manifest under
  `w17/services/` (or through `W17_BUSINESS_MANIFEST`, which the image sets)
  and compiles the descriptor set it names into `OUT_DIR`.
- `src/main.rs` is `w17_business::main!(register_business);`, the
  `pb` module (`w17_business::include_protos!()`), and the implementations.
  The binary refuses to serve a manifest whose descriptor set differs from
  the one it was compiled from: rebuild it after every codegen.
- Every handler reads its context with `w17_business::ctx(&req)` and builds
  each call it makes with `ctx.request(msg)`; the SDK's channels refuse a
  request without one.

## The end-to-end suite

`w17/e2e/notes/tests/rest/` asserts, over REST against the running stack:

| case | what |
|---|---|
| `0010` | a note written by the Rust tier; `note.created` and the subscriber's `note.delivered` both arrive on `/w17-events` |
| `0020`, `0042`, `0052`, `0054` | the committed write in a transaction exists, `on_commit` ran once after the commit, and the event held for the commit was delivered |
| `0022`, `0024` | the rolled-back write is refused, no `note.created` arrives, and the batch it wrote into is empty |
| `0030` → `0032` → `0034` | page 1, page 2 and back to page 1 of `ListNotes`, through the Rust tier |
| `0040`, `0050`, `0060` | the note is stored, the subscriber marked it exactly once, the storage query pages the same |
| `0070` → `0072` | `ExportNote` stores a file through the companion; downloaded through the gateway, it holds the bytes the Rust tier rendered |
| `0080`, `0082` | a quote from the Quotes stand-in through the egress client; an unknown one is a 404 |

## Running it

```sh
w17ctl login            # once
w17ctl codegen          # generate the services, the manifest and the descriptor set
w17ctl test             # bring the stack up, run the e2e suite, tear it down
```

The business image builds the crate with Cargo against the Rust business SDK
at the tag `rs/<sdk_version>` its `Cargo.lock` names. Generated against a
wandering-compiler checkout (`W17_WANDERING_COMPILER_PATH`), it builds the
checkout's `sdk/rs/w17-business` instead, served to Cargo under the tag the
lock names.
