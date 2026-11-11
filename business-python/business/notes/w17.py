"""The notes domain's business tier, written in Python.

Codegen writes no code for it: w17/services/notes-business/ holds the manifest
(w17-business.json) and the compiled protos (descriptors.binpb), and the Python
business SDK (`w17_business`) builds the gRPC services from them at boot. This
module is all the project writes: one `register_business` and one class per
business service, whose methods are named exactly as the RPCs.

Every handler takes the request context first and passes it on to every call:
it carries the caller's metadata, the deadline and the open transaction.

What a Go tier runs in-process — the egress clients of proto/clients/ and the
upload file stores — this tier reaches through the business companion, a
generated Go process beside it: `clients.egress("quotes")` and
`clients.store("notes-exports")`.
"""

from __future__ import annotations

import contextlib

from w17_business import Status, distx, for_user, on_commit, paging

NOTES = "business_python.notes.board.NotesService"
EVENTS = "business_python.notes.board.NotesEvents"
QUERY = "business_python.notes.board.NoteQuery"
MUTATION = "business_python.notes.board.NoteMutation"

ROLLBACK_PREFIX = "rollback:"

# The table field ExportNote's file is for, by full proto name: it names the
# placement (bucket path, sub-bucketing, size cap) and, through it, the
# connection (notes-exports).
EXPORT_FIELD = "business_python.notes.board.NoteExport.file"


def note_text(note) -> bytes:
    """A note rendered as the text file ExportNote stores."""
    return f"{note.title}\n\n{note.body}\n".encode()


class NotesService:
    def __init__(self, registry, clients):
        self.registry = registry
        self.query = clients.service(QUERY)
        self.mutation = clients.service(MUTATION)
        self.tx = clients.distributed_transaction()
        # The business companion: the egress client proto/clients/quotes and
        # the file store of notes-exports, the connection this tier writes.
        self.quotes = clients.egress("quotes")
        self.exports = clients.store("notes-exports")
        msg = registry.pool.message
        self.CreateNoteReq = msg("business_python.notes.board.CreateNoteReq")
        self.RecordMarkReq = msg("business_python.notes.board.RecordMarkReq")
        self.RecordNoteExportReq = msg("business_python.notes.board.RecordNoteExportReq")
        self.GetNoteReq = msg("business_python.notes.board.GetNoteReq")
        self.ListBatchReq = msg("business_python.notes.board.ListBatchReq")
        self.BrowseNotesResp = msg("business_python.notes.board.BrowseNotesResp")
        self.GetQuoteRequest = msg("clients.quotes.GetQuoteRequest")
        self.QuoteResp = msg("business_python.notes.board.QuoteResp")

    async def CreateNote(self, ctx, req):
        # The storage write. NoteCreated is declared on this method, so the
        # SDK publishes it once this returns.
        return await self.mutation.CreateNote(
            ctx, self.CreateNoteReq(batch=req.batch, title=req.title, body=req.body))

    async def CreateNoteInTx(self, ctx, req):
        async def work(tx_ctx):
            # Composed through the SERVED service, never `self.CreateNote`:
            # a direct call would skip the method's NoteCreated. Inside the
            # transaction the event waits for the commit.
            note = await self.registry.self(NOTES).CreateNote(tx_ctx, req)

            async def committed():
                # Runs after the root transaction committed, never after a
                # rollback. The write it makes is not part of the
                # transaction (it has landed), so it goes out on the
                # caller's context.
                await self.mutation.RecordMark(ctx, self.RecordMarkReq(note_id=note.id, mark="committed"))

            await on_commit(tx_ctx, committed)
            if req.title.startswith(ROLLBACK_PREFIX):
                # After the write: the rollback is what removes it.
                raise for_user(Status.FAILED_PRECONDITION, "ROLLED_BACK",
                               "the note was written and rolled back, as its title asked")
            return note

        # The transaction opens on the domain's connection, by its lock name.
        return await distx.run(ctx, self.tx, distx.begin_request("notes-postgres"), work)

    async def ListNotes(self, ctx, req):
        # The page the caller asked for (cursor, page size) arrived as
        # incoming metadata. paging.forward puts it on THIS call only.
        page = await self.query.ListBatch(paging.forward(ctx), self.ListBatchReq(batch=req.batch))
        resp = self.BrowseNotesResp()
        resp.items.extend(page.items)
        if page.HasField("paging"):
            resp.paging.CopyFrom(page.paging)
        return resp

    async def ExportNote(self, ctx, req):
        # Renders the note as a text file, PUTS it on this tier's own
        # connection and records the handle. The companion derives the key
        # from NoteExport.file's placement (its (w17.field).upload), so
        # nothing here names a bucket path: the handle is what a Go tier's
        # kvfs.Put would return.
        note = await self.query.GetNote(ctx, self.GetNoteReq(note_id=req.note_id))
        handle = await self.exports.put(ctx, EXPORT_FIELD, note_text(note))
        try:
            return await self.mutation.RecordNoteExport(
                ctx, self.RecordNoteExportReq(note_id=note.id, file=handle))
        except BaseException:
            # No row names the object, so nothing would ever read or delete
            # it: remove what this call stored. A file is not part of a
            # storage transaction, in Go either. Detached from the caller's
            # cancellation, so a hang-up does not leave the file behind; its
            # own failure must not hide the error that got us here.
            with contextlib.suppress(Exception):
                await self.exports.delete(ctx.without_cancel(), handle)
            raise

    async def Quote(self, ctx, req):
        # An ordinary gRPC call: the companion makes the HTTP request with the
        # credential from W17_CLIENT_QUOTES_DSN, retries what is safe to retry
        # and codes the API's errors for this caller. An unknown quote comes
        # back NOT_FOUND and passes through as it is.
        q = await self.quotes.GetQuote(ctx, self.GetQuoteRequest(id=req.id))
        return self.QuoteResp(id=q.id, text=q.text, author=q.author)


class NotesEvents:
    def __init__(self, registry, clients):
        self.mutation = clients.service(MUTATION)
        self.RecordMarkReq = registry.pool.message("business_python.notes.board.RecordMarkReq")
        self.HandlerAck = registry.pool.message("business_python.notes.board.HandlerAck")

    async def OnNoteCreated(self, ctx, event):
        await self.mutation.RecordMark(ctx, self.RecordMarkReq(note_id=event.note_id, mark="delivered"))
        return self.HandlerAck(note_id=event.note_id)


def register_business(cfg, registry, clients):
    registry.register(NOTES, NotesService(registry, clients))
    registry.register(EVENTS, NotesEvents(registry, clients))
