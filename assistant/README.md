# assistant

Notes, and an assistant that answers questions about them: the `agent` plugin
for the language-model calls (and for recording what each one cost), the
`auth` plugin for accounts, and one hand-written service.

- **Notes** (`POST /api/v1/notes`, `proto/domains/assistant/notes/`): every
  note belongs to the account that wrote it. The model declares a data scope
  (`(w17.db.scope)` on `Note`), so storage stamps the owner on each insert and
  filters each read by it; no handler code does that.
- **The assistant** (the `business` package in this project's Go module, `srcgo`), a hand-written
  service over the agent plugin. It decides the model, the instructions and
  whose bill a call goes on, so a caller can ask questions and nothing else:
  - `POST /api/v1/assistant/ask` — one question, one answer, and its tokens;
  - `GET /api/v1/assistant/ask/stream?question=…` — the same as server-sent
    events, as the answer is written;
  - `POST /api/v1/assistant/research` — the model may call a tool,
    `search_notes`, which this service runs as the caller, so it only ever
    finds the caller's own notes;
  - `GET /api/v1/assistant/spend` — what the caller's questions have cost.

The plugins are pinned to a commit on the plugins repository's `main`
(`w17/lock.yaml`), not a release, until a release carries what the example
needs.

## The provider

`fakellm/` is a stand-in for the provider (the Responses API as an Azure
OpenAI deployment serves it), so the example runs without a provider account.
Its answers are deterministic ("Answer: <question>", tokens counted in words),
and a marker in a question selects a refusal (`[error:400]`) or an answer cut
at the output budget (`[incomplete]`).

It serves HTTPS only. The agent plugin sends its API key over plain HTTP to
nothing but loopback, and an example should not need a switch that a
production deployment could leave on. So the fake makes its own certificate
authority at start and writes it to a volume; `compose.assistant.yaml` mounts
it into the storage bundle, which runs the plugin, and trusts it there with
`SSL_CERT_FILE`.

## Tests

```sh
w17ctl codegen
w17ctl test          # the generated end-to-end suite (w17/e2e/assistant/)
livetest/run.sh      # the live suite
```

The end-to-end suite covers the API: notes and their owner, a question and
its tokens, an empty question, an answer cut short, the provider failing, the
streamed answer frame by frame, a research run that searches the caller's
notes and one that cannot see another account's, and the spend of a new
account.

`livetest/` checks what only the running stack shows:

- each question writes one usage row under the model the provider named, with
  the tokens it counted, and a research run writes one per model call;
- the spend report adds them up once they land (the plugin records usage
  asynchronously);
- six first questions at once from one account make one usage scope;
- a scope capped at nothing is refused before any request reaches the
  provider;
- every request the provider received asks not to be stored, and carries the
  project's instructions and model;
- a refusal that quotes the prompt reaches the caller without the quote, and
  `run.sh` checks that no log carries the provider key or the quoted prompt.
