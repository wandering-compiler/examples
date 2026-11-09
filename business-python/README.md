# business-python

One domain whose storage tier is generated and whose **business tier is
written in Python**. Codegen writes no Python: it writes the tier's manifest
(`w17/services/notes-business/w17-business.json`) and the compiled protos
(`descriptors.binpb`), and the [Python business SDK](https://github.com/wandering-compiler/sdk/tree/main/py)
builds the gRPC services from them at boot. The only code in the project is
`business/notes/w17.py`.

```yaml
# w17/lock.yaml
generated_code:
  business_bundles:
    - domain: notes
      language: python
      register: business/notes
```

which is what `w17ctl target business add --domain notes --language python
--register business/notes` writes.

## What it shows

- **Notes** on Postgres (`proto/domains/notes/board/`): a `Note` model, a
  create mutation and a paged list query, all generated.
- **The Python business tier** (`NotesService`, `business/notes/w17.py`):
  - `CreateNote` writes through the storage mutation and carries
    `(w17.event_emit)`: the SDK publishes `NoteCreated` on NATS after the
    method returns.
  - `CreateNoteInTx` runs `CreateNote` inside a distributed transaction
    (`distx.run`), composed through the served service so its event waits
    for the commit. A title starting with `rollback:` makes it fail after the
    write: the transaction rolls back, the note is gone and no event is
    published. On commit, an `on_commit` hook records a `committed` mark.
  - `ListNotes` serves a paged REST endpoint: it calls the storage query with
    `paging.forward(ctx)` and copies the query's `w17.Paging`. It knows
    nothing of how storage sorts the page: storage reports where the page
    ends, the SDK relays it, and the gateway builds the next and previous
    cursor from it.
- **A subscriber in the same Python tier** (`NotesEvents.OnNoteCreated`,
  `subscribers.proto`): the generated eventbus runner delivers every
  `NoteCreated` to it; it records a `delivered` mark and publishes
  `NoteDelivered`.
- **The business companion** (`w17/services/notes-business-companion/`,
  generated): what a Go tier runs in-process — the egress clients of
  `proto/clients/` and the upload file stores — served over gRPC to the
  Python tier by the same Go code. Its own container here; folded into the
  domain's binary when the domain is composed.
  - `Quote` calls the third-party Quotes API through the generated egress
    client `proto/clients/quotes` (`clients.egress("quotes")`). The
    companion makes the HTTP request with the credential from
    `W17_CLIENT_QUOTES_DSN` and codes the API's errors for the caller: an
    unknown quote is `NOT_FOUND`. `quotestub/` stands in for the API in
    `compose.yaml`.
  - `ExportNote` renders a note as a text file, puts it on its own
    connection `notes-exports` (`clients.store("notes-exports")`, the
    placement of the `writer: BUSINESS` field `NoteExport.file`) and
    records the handle through a storage mutation. The gateway serves it
    back through the `/files` download route. Gateway and companion are
    separate containers, so `compose.yaml` mounts one `notes-exports`
    volume into both: one connection is one store.
- **REST** for all of it (`proto/domains/notes/rest.proto`), without
  authentication.

## The end-to-end suite

`w17/e2e/notes/tests/rest/` asserts, over REST against the running stack:

| case | what |
|---|---|
| `0010` | a note written by the Python tier; `note.created` and the subscriber's `note.delivered` both arrive on `/w17-events` |
| `0020`, `0042`, `0052`, `0054` | the committed write in a transaction exists, `on_commit` ran once after the commit, and the event held for the commit was delivered |
| `0022`, `0024` | the rolled-back write is refused, no `note.created` arrives, and the batch it wrote into is empty |
| `0030` → `0032` → `0034` | page 1, page 2 and back to page 1 of `ListNotes`, through the Python tier |
| `0040`, `0050`, `0060` | the note is stored, the subscriber marked it exactly once, the storage query pages the same |
| `0070` → `0072` | `ExportNote` stores a file through the companion; downloaded through the gateway, it holds the bytes the Python tier rendered |
| `0080`, `0082` | a quote from the Quotes stand-in through the egress client; an unknown one is a 404 |

## Running it

```sh
w17ctl login            # once
w17ctl codegen          # generate the services, the manifest and the descriptor set
w17ctl test             # bring the stack up, run the e2e suite, tear it down
```

The business image installs the Python business SDK at the lock's
`sdk_version` (`py/<sdk_version>` of the sdk repository). Generated against a
wandering-compiler checkout (`W17_WANDERING_COMPILER_PATH`), it installs the
checkout's `sdk/py` instead.
