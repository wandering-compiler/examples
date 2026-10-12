# jobs

Small jobs run on a pool of workers. The `cluster` plugin is the pool's
control plane: it knows the relays the workers attach to, places each job on
one of them, and hands the caller a single-use ticket. What the workers do is
this project's business. Here they upper-case text.

```
caller ──REST──▶ jobs-gateway ─▶ Jobs (business) ──ScheduleTask──▶ cluster plugin (jobs-storage)
                                      │                                  │ manages (mTLS, pinned)
                                      │ ticket, pinned TLS               ▼
                                      └──────────────────────────────▶ relay ◀── worker-a, worker-b
                                                                         (attach + tunnel, pinned)
```

- **`Jobs.Shout`** (`POST /api/v1/jobs/shout`), hand-written in the `business`
  package of the project's Go module (`srcgo`). It holds the plugin's
  `ScheduleTask` stream until a slot is granted, then calls the worker through
  the relay the slot is on. The call is TLS-pinned to the fingerprint the grant
  named and carries the ticket. The worker's answer and its name come back.
- **The workers** (`worker/`) use the plugin's worker library
  (`lib/workerconn`). They attach to the relay, report one slot each, and serve
  `jobs.Worker/Shout` on a tunnel they dial themselves, so a worker never has to
  be reachable. The contract between the service and the workers is in
  `srcgo/workapi`.
- **The relay** (`relay/`) is built from the plugin tree vendored into this
  project (`proto/plugins/cluster`). It is the same program an operator runs in
  production.

Every part pins the others, and `compose.jobs.yaml` sets the pins up before
anything starts:

- `identities` (one-shot, `relay/setup.sh`) mints the control plane's client
  identity, the relay's identity and the authority it issues worker
  certificates from, and a certificate for each worker. Each key stays in the
  volume of the service that presents it; the fingerprints the others pin go
  to a volume of their own (`pins`), which is all the relay and `register`
  mount.
- `relay` starts pinned to the control plane's fingerprint, so nothing else
  may manage it.
- `register` (one-shot, `register/`) puts the relay in the plugin's registry
  with its fingerprint, as an operator does on the Relays page of the admin.
- `jobs-storage` presents the control plane's identity to the relay
  (`JOBS_WORKERS_CLIENT_CERT_PATH`).

The workers here get their certificates straight from the relay's authority.
In a real pool a worker enrols with a one-time registration code the admin
issues. The plugin's own dev sandbox (`clusterrelay`) is switched off, because
this example builds the relay itself.

The plugins are pinned to a commit on the plugins repository's `main`
(`w17/lock.yaml`), not a release, until a release carries what the example
needs.

## Tests

```sh
w17ctl codegen
w17ctl test          # the generated end-to-end suite (w17/e2e/jobs/)
livetest/run.sh      # the live suite
```

The end-to-end suite runs a job through the whole chain and checks the
answer and the worker that ran it, that an empty job is refused, and that the
relay forwards any payload unchanged.

`livetest/` checks the pool itself:

- two jobs at once run on both workers;
- a third job on the full pool waits for a slot and then runs;
- a ticket admits one call, and a replay is refused with
  `TICKET_NOT_REDEEMABLE`;
- a ticket nobody claims within the relay's ticket TTL (5 s here) is refused,
  and its slot goes back to the pool;
- after a worker is banned, jobs run only on the other one; the ban is
  recorded under the operator who made it.
