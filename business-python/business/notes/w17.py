"""The notes domain's business tier, written in Python.

Codegen writes no code for it: w17/services/notes-business/ holds the manifest
(w17-business.json) and the compiled protos (descriptors.binpb), and the Python
business SDK (`w17_business`) builds the gRPC services from them at boot. This
module is all the project writes: one `register_business` and one class per
business service, whose methods are named exactly as the RPCs.

Every handler takes the request context first and passes it on to every call:
it carries the caller's metadata, the deadline and the open transaction.
"""

from __future__ import annotations

from w17_business import Status, distx, for_user, on_commit, paging

NOTES = "business_python.notes.board.NotesService"
EVENTS = "business_python.notes.board.NotesEvents"
QUERY = "business_python.notes.board.NoteQuery"
MUTATION = "business_python.notes.board.NoteMutation"

ROLLBACK_PREFIX = "rollback:"


class NotesService:
    def __init__(self, registry, clients):
        self.registry = registry
        self.query = clients.service(QUERY)
        self.mutation = clients.service(MUTATION)
        self.tx = clients.distributed_transaction()
        msg = registry.pool.message
        self.CreateNoteReq = msg("business_python.notes.board.CreateNoteReq")
        self.RecordMarkReq = msg("business_python.notes.board.RecordMarkReq")
        self.ListBatchReq = msg("business_python.notes.board.ListBatchReq")
        self.BrowseNotesResp = msg("business_python.notes.board.BrowseNotesResp")

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
