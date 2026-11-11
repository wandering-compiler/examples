// The notes domain's business tier, written in TypeScript.
//
// Codegen writes no code for it: w17/services/notes-business/ holds the
// manifest (w17-business.json) and the compiled protos (descriptors.binpb),
// and the TypeScript business SDK (`@w17/business`) builds the gRPC services
// from them at boot. This module is all the project writes: one
// `registerBusiness` and one class per business service, whose methods are
// named exactly as the RPCs.
//
// Node runs it as it is (type stripping, no build step), so it is erasable
// TypeScript only: no enums, no namespaces, no constructor parameter
// properties, and type-only imports through `import type`.
//
// Every handler takes the request context first and passes it on to every
// call: it carries the caller's metadata, the deadline and the open
// transaction.
//
// What a Go tier runs in-process — the egress clients of proto/clients/ and
// the upload file stores — this tier reaches through the business companion,
// a generated Go process beside it: `clients.egress("quotes")` and
// `clients.store("notes-exports")`.

import { distx, forUser, onCommit, paging, Status } from "@w17/business";
import type {
  AnyMessage,
  Clients,
  Config,
  Context,
  FileStore,
  MessageType,
  Registry,
  ServiceClient,
  TxCoordinator,
} from "@w17/business";

const PKG = "business_ts.notes.board.";
const NOTES = PKG + "NotesService";
const EVENTS = PKG + "NotesEvents";
const QUERY = PKG + "NoteQuery";
const MUTATION = PKG + "NoteMutation";

const ROLLBACK_PREFIX = "rollback:";

// The table field ExportNote's file is for, by full proto name: it names the
// placement (bucket path, sub-bucketing, size cap) and, through it, the
// connection (notes-exports).
const EXPORT_FIELD = PKG + "NoteExport.file";

// A note rendered as the text file ExportNote stores.
function noteText(note: AnyMessage): Uint8Array {
  return new TextEncoder().encode(`${note.title}\n\n${note.body}\n`);
}

class NotesService {
  registry: Registry;
  query: ServiceClient;
  mutation: ServiceClient;
  tx: TxCoordinator;
  quotes: ServiceClient;
  exports: FileStore;
  CreateNoteReq: MessageType;
  RecordMarkReq: MessageType;
  RecordNoteExportReq: MessageType;
  GetNoteReq: MessageType;
  ListBatchReq: MessageType;
  BrowseNotesResp: MessageType;
  GetQuoteRequest: MessageType;
  QuoteResp: MessageType;

  constructor(registry: Registry, clients: Clients) {
    this.registry = registry;
    this.query = clients.service(QUERY);
    this.mutation = clients.service(MUTATION);
    this.tx = clients.distributedTransaction();
    // The business companion: the egress client proto/clients/quotes and the
    // file store of notes-exports, the connection this tier writes.
    this.quotes = clients.egress("quotes");
    this.exports = clients.store("notes-exports");
    const msg = (name: string) => registry.pool.message(PKG + name);
    this.CreateNoteReq = msg("CreateNoteReq");
    this.RecordMarkReq = msg("RecordMarkReq");
    this.RecordNoteExportReq = msg("RecordNoteExportReq");
    this.GetNoteReq = msg("GetNoteReq");
    this.ListBatchReq = msg("ListBatchReq");
    this.BrowseNotesResp = msg("BrowseNotesResp");
    this.GetQuoteRequest = registry.pool.message("clients.quotes.GetQuoteRequest");
    this.QuoteResp = msg("QuoteResp");
  }

  async CreateNote(ctx: Context, req: AnyMessage): Promise<AnyMessage> {
    // The storage write. NoteCreated is declared on this method, so the SDK
    // publishes it once this returns.
    return this.mutation.CreateNote(ctx, this.CreateNoteReq({ batch: req.batch, title: req.title, body: req.body }));
  }

  async CreateNoteInTx(ctx: Context, req: AnyMessage): Promise<AnyMessage> {
    // The transaction opens on the domain's connection, by its lock name.
    return distx.run(ctx, this.tx, distx.beginRequest("notes-postgres"), async (txCtx) => {
      // Composed through the SERVED service, never `this.CreateNote`: a
      // direct call would skip the method's NoteCreated. Inside the
      // transaction the event waits for the commit.
      const note = await this.registry.self(NOTES).CreateNote(txCtx, req);

      // Runs after the root transaction committed, never after a rollback.
      // The write it makes is not part of the transaction (it has landed), so
      // it goes out on the caller's context, detached from its cancellation:
      // work after a commit runs to its end even when the caller hangs up.
      const detached = ctx.withoutCancel();
      await onCommit(txCtx, () =>
        this.mutation.RecordMark(detached, this.RecordMarkReq({ noteId: note.id, mark: "committed" })),
      );

      if ((req.title as string).startsWith(ROLLBACK_PREFIX)) {
        // After the write: the rollback is what removes it.
        throw forUser(
          Status.FAILED_PRECONDITION,
          "ROLLED_BACK",
          "the note was written and rolled back, as its title asked",
        );
      }
      return note;
    });
  }

  async ListNotes(ctx: Context, req: AnyMessage): Promise<AnyMessage> {
    // The page the caller asked for (cursor, page size) arrived as incoming
    // metadata. paging.forward puts it on THIS call only.
    const page = await this.query.ListBatch(paging.forward(ctx), this.ListBatchReq({ batch: req.batch }));
    return this.BrowseNotesResp({ items: page.items, paging: page.paging });
  }

  async ExportNote(ctx: Context, req: AnyMessage): Promise<AnyMessage> {
    // Renders the note as a text file, PUTS it on this tier's own connection
    // and records the handle. The companion derives the key from
    // NoteExport.file's placement (its (w17.field).upload), so nothing here
    // names a bucket path: the handle is what a Go tier's kvfs.Put would
    // return.
    const note = await this.query.GetNote(ctx, this.GetNoteReq({ noteId: req.noteId }));
    const handle = await this.exports.put(ctx, EXPORT_FIELD, noteText(note));
    try {
      return await this.mutation.RecordNoteExport(ctx, this.RecordNoteExportReq({ noteId: note.id, file: handle }));
    } catch (err) {
      // No row names the object, so nothing would ever read or delete it:
      // remove what this call stored. A file is not part of a storage
      // transaction, in Go either. Detached from the caller's cancellation,
      // so a hang-up does not leave the file behind; its own failure must not
      // hide the error that got us here.
      await this.exports.delete(ctx.withoutCancel(), handle).catch(() => undefined);
      throw err;
    }
  }

  async Quote(ctx: Context, req: AnyMessage): Promise<AnyMessage> {
    // An ordinary gRPC call: the companion makes the HTTP request with the
    // credential from W17_CLIENT_QUOTES_DSN, retries what is safe to retry
    // and codes the API's errors for this caller. An unknown quote comes back
    // NOT_FOUND and passes through as it is.
    const q = await this.quotes.GetQuote(ctx, this.GetQuoteRequest({ id: req.id }));
    return this.QuoteResp({ id: q.id, text: q.text, author: q.author });
  }
}

class NotesEvents {
  mutation: ServiceClient;
  RecordMarkReq: MessageType;
  HandlerAck: MessageType;

  constructor(registry: Registry, clients: Clients) {
    this.mutation = clients.service(MUTATION);
    this.RecordMarkReq = registry.pool.message(PKG + "RecordMarkReq");
    this.HandlerAck = registry.pool.message(PKG + "HandlerAck");
  }

  async OnNoteCreated(ctx: Context, event: AnyMessage): Promise<AnyMessage> {
    await this.mutation.RecordMark(ctx, this.RecordMarkReq({ noteId: event.noteId, mark: "delivered" }));
    return this.HandlerAck({ noteId: event.noteId });
  }
}

export function registerBusiness(_cfg: Config, registry: Registry, clients: Clients): void {
  registry.register(NOTES, new NotesService(registry, clients));
  registry.register(EVENTS, new NotesEvents(registry, clients));
}
