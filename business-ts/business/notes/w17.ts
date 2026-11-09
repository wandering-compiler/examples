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

import { distx, forUser, onCommit, paging, Status } from "@w17/business";
import type {
  AnyMessage,
  Clients,
  Config,
  Context,
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

class NotesService {
  registry: Registry;
  query: ServiceClient;
  mutation: ServiceClient;
  tx: TxCoordinator;
  CreateNoteReq: MessageType;
  RecordMarkReq: MessageType;
  ListBatchReq: MessageType;
  BrowseNotesResp: MessageType;

  constructor(registry: Registry, clients: Clients) {
    this.registry = registry;
    this.query = clients.service(QUERY);
    this.mutation = clients.service(MUTATION);
    this.tx = clients.distributedTransaction();
    const msg = (name: string) => registry.pool.message(PKG + name);
    this.CreateNoteReq = msg("CreateNoteReq");
    this.RecordMarkReq = msg("RecordMarkReq");
    this.ListBatchReq = msg("ListBatchReq");
    this.BrowseNotesResp = msg("BrowseNotesResp");
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
