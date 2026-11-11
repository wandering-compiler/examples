//! The notes domain's business tier, written in Rust.
//!
//! Codegen writes no Rust for it: w17/services/notes-business/ holds the
//! manifest (w17-business.json) and the compiled protos (descriptors.binpb).
//! build.rs compiles the descriptor set into the tonic traits and clients of
//! `pb`, and the Rust business SDK (`w17-business`) serves the
//! implementations below with the middleware every business tier gets. A
//! method missing here, or one whose types drifted from the proto, is a
//! compile error.
//!
//! Every handler takes its context from the request (`w17_business::ctx`)
//! and builds every call it makes with `ctx.request(msg)`: the context
//! carries the caller's metadata, the deadline and the open transaction.
//!
//! What a Go tier runs in-process — the egress clients of proto/clients/ and
//! the upload file stores — this tier reaches through the business
//! companion, a generated Go process beside it: `clients.egress("quotes")`
//! and `clients.store("notes-exports")`.

mod pb {
    w17_business::include_protos!();
}

use pb::business_rs::notes::board::{
    BrowseNotesReq, BrowseNotesResp, CreateNoteReq, ExportNoteReq, GetNoteReq, HandlerAck,
    ListBatchReq, Note, NoteCreated, NoteExport, QuoteReq, QuoteResp, RecordMarkReq,
    RecordNoteExportReq, WriteNoteReq,
    note_mutation_client::NoteMutationClient,
    note_query_client::NoteQueryClient,
    notes_events_server::{NotesEvents, NotesEventsServer},
    notes_service_client::NotesServiceClient,
    notes_service_server::{NotesService, NotesServiceServer},
};
use pb::clients::quotes::{GetQuoteRequest, quotes_api_client::QuotesApiClient};
use w17_business::{
    BootError, Clients, Code, CompanionChannel, Config, Ctx, Registry, Request, Response,
    SelfChannel, Status, StorageChannel, distx, files::FileStore, for_user, on_commit, paging,
};

w17_business::main!(register_business);

// The connection the transaction opens on, by its lock name.
const CONNECTION: &str = "notes-postgres";

const ROLLBACK_PREFIX: &str = "rollback:";

// The table field ExportNote's file is for, by full proto name: it names the
// placement (bucket path, sub-bucketing, size cap) and, through it, the
// connection (notes-exports).
const EXPORT_FIELD: &str = "business_rs.notes.board.NoteExport.file";

fn register_business(
    _cfg: &Config,
    registry: &mut Registry,
    clients: &Clients,
) -> Result<(), BootError> {
    let query = NoteQueryClient::new(clients.storage());
    let mutation = NoteMutationClient::new(clients.storage());
    registry.register(NotesServiceServer::new(Notes {
        query,
        mutation: mutation.clone(),
        tx: clients.distributed_transaction()?,
        // Composition goes through the SERVED service, so its emit fires.
        me: NotesServiceClient::new(registry.self_channel()),
        // The business companion: the egress client proto/clients/quotes and
        // the file store of notes-exports, the connection this tier writes.
        quotes: QuotesApiClient::new(clients.egress("quotes")?),
        exports: clients.store("notes-exports")?,
    }))?;
    registry.register(NotesEventsServer::new(Events { mutation }))
}

// A note rendered as the text file ExportNote stores.
fn note_text(note: &Note) -> Vec<u8> {
    format!("{}\n\n{}\n", note.title, note.body).into_bytes()
}

struct Notes {
    query: NoteQueryClient<StorageChannel>,
    mutation: NoteMutationClient<StorageChannel>,
    tx: distx::Coordinator,
    me: NotesServiceClient<SelfChannel>,
    quotes: QuotesApiClient<CompanionChannel>,
    exports: FileStore,
}

#[tonic::async_trait]
impl NotesService for Notes {
    async fn create_note(&self, req: Request<WriteNoteReq>) -> Result<Response<Note>, Status> {
        // The storage write. NoteCreated is declared on this method, so the
        // SDK publishes it once this returns.
        let ctx = w17_business::ctx(&req)?;
        let r = req.into_inner();
        let write = CreateNoteReq {
            batch: r.batch,
            title: r.title,
            body: r.body,
        };
        self.mutation.clone().create_note(ctx.request(write)).await
    }

    async fn create_note_in_tx(
        &self,
        req: Request<WriteNoteReq>,
    ) -> Result<Response<Note>, Status> {
        let ctx = w17_business::ctx(&req)?;
        let r = req.into_inner();
        let note = distx::run(
            &ctx,
            &self.tx,
            distx::begin_request(CONNECTION),
            async |tx_ctx: Ctx| {
                // Composed through the served service, never
                // `self.create_note`: a direct call would skip the method's
                // NoteCreated. Inside the transaction the event waits for the
                // commit.
                let note = self
                    .me
                    .clone()
                    .create_note(tx_ctx.request(r.clone()))
                    .await?
                    .into_inner();

                // Runs after the root transaction committed, never after a
                // rollback. The write it makes is not part of the transaction
                // (it has landed), so it goes out on the caller's context,
                // detached from its cancellation: work after a commit runs to
                // its end even when the caller hangs up.
                let detached = ctx.without_cancel();
                let mutation = self.mutation.clone();
                let mark = RecordMarkReq {
                    note_id: note.id,
                    mark: "committed".into(),
                };
                // Inside the transaction this parks the work (Deferred) and
                // returns at once; an Err is only possible outside one.
                on_commit(&tx_ctx, async move {
                    mutation.clone().record_mark(detached.request(mark)).await
                })
                .await
                .map_err(Status::internal)?;

                if r.title.starts_with(ROLLBACK_PREFIX) {
                    // After the write: the rollback is what removes it.
                    return Err(for_user(
                        Code::FailedPrecondition,
                        "ROLLED_BACK",
                        "the note was written and rolled back, as its title asked",
                        "",
                    ));
                }
                Ok(note)
            },
        )
        .await?;
        Ok(Response::new(note))
    }

    async fn list_notes(
        &self,
        req: Request<BrowseNotesReq>,
    ) -> Result<Response<BrowseNotesResp>, Status> {
        // The page the caller asked for (cursor, page size) arrived as
        // incoming metadata. paging::forward puts it on THIS call only.
        let ctx = w17_business::ctx(&req)?;
        let batch = req.into_inner().batch;
        let page = self
            .query
            .clone()
            .list_batch(paging::forward(&ctx).request(ListBatchReq { batch }))
            .await?
            .into_inner();
        Ok(Response::new(BrowseNotesResp {
            items: page.items,
            paging: page.paging,
        }))
    }

    async fn export_note(
        &self,
        req: Request<ExportNoteReq>,
    ) -> Result<Response<NoteExport>, Status> {
        // Renders the note as a text file, PUTS it on this tier's own
        // connection and records the handle. The companion derives the key
        // from NoteExport.file's placement (its (w17.field).upload), so
        // nothing here names a bucket path: the handle is what a Go tier's
        // kvfs.Put would return.
        let ctx = w17_business::ctx(&req)?;
        let note_id = req.into_inner().note_id;
        let note = self
            .query
            .clone()
            .get_note(ctx.request(GetNoteReq { note_id }))
            .await?
            .into_inner();
        let handle = self
            .exports
            .put(&ctx, EXPORT_FIELD, note_text(&note))
            .await?;
        let record = RecordNoteExportReq {
            note_id: note.id,
            file: handle.clone(),
        };
        match self
            .mutation
            .clone()
            .record_note_export(ctx.request(record))
            .await
        {
            Ok(export) => Ok(export),
            Err(err) => {
                // No row names the object, so nothing would ever read or
                // delete it: remove what this call stored. A file is not part
                // of a storage transaction, in Go either. Detached from the
                // caller's cancellation, so a hang-up does not leave the file
                // behind; its own failure must not hide the error that got us
                // here.
                let _ = self.exports.delete(&ctx.without_cancel(), &handle).await;
                Err(err)
            }
        }
    }

    async fn quote(&self, req: Request<QuoteReq>) -> Result<Response<QuoteResp>, Status> {
        // An ordinary gRPC call: the companion makes the HTTP request with
        // the credential from W17_CLIENT_QUOTES_DSN, retries what is safe to
        // retry and codes the API's errors for this caller. An unknown quote
        // comes back NOT_FOUND and passes through as it is.
        let ctx = w17_business::ctx(&req)?;
        let id = req.into_inner().id;
        let q = self
            .quotes
            .clone()
            .get_quote(ctx.request(GetQuoteRequest { id }))
            .await?
            .into_inner();
        Ok(Response::new(QuoteResp {
            id: q.id,
            text: q.text,
            author: q.author.unwrap_or_default(),
        }))
    }
}

struct Events {
    mutation: NoteMutationClient<StorageChannel>,
}

#[tonic::async_trait]
impl NotesEvents for Events {
    async fn on_note_created(
        &self,
        req: Request<NoteCreated>,
    ) -> Result<Response<HandlerAck>, Status> {
        let ctx = w17_business::ctx(&req)?;
        let note_id = req.into_inner().note_id;
        let mark = RecordMarkReq {
            note_id,
            mark: "delivered".into(),
        };
        self.mutation.clone().record_mark(ctx.request(mark)).await?;
        Ok(Response::new(HandlerAck { note_id }))
    }
}
