# rpc-cli

One domain, `ops`, with jobs on Postgres and two surfaces besides the usual
ones: a public gRPC API (`(w17.rpc_api)`) and a generated command-line tool
(`(w17.cli_api)`).

## The public gRPC surface

`proto/domains/ops/rpc.proto` declares it. The gateway serves it on :8082,
beside REST on :8080, under public service names of its own:

| Public service | Method | Picked by |
|---|---|---|
| `JobRead` | `GetJob`, `ListJobs` | the `JobRead` group in `rpc.proto` (`refs`) |
| `JobAdmin` | `CreateJob` | `(w17.rpc)` and `(w17.rpc_service) = "JobAdmin"` on the method |
| `JobMutation` | `CancelJob` | `(w17.rpc)` on the method |
| — | `CountJobs` | nothing |

- The surface is the methods picked, not whole services: `CountJobs` is on
  the same backend service as `GetJob` and is not served.
- `CreateJob` and `CancelJob` are on one backend service, `JobMutation`.
  `(w17.rpc_service)` moves `CreateJob` to `JobAdmin`; `CancelJob` keeps
  the backend's name.
- The generated contract is `proto/domains/ops/w17.lock.rpc.proto`
  (package `w17lock.ops.rpc`). `public_fqn` gives `JobRead` a second name,
  `acme.ops.v1.JobRead`, for clients built from a contract published under
  another package.
- `reflection: true` lets grpcurl and similar tools list and call the
  surface without the contract.

## The CLI

`proto/domains/ops/cli.proto` declares it, and `(w17.cli)` on a method
gives it a command (`job get <job_id>`, `job list`, …). Two binaries come
out of it:

- **`ops-gateway-cli`** (`w17/services/ops-gateway/cmd/`) dials the backend
  services directly, like the gateway does, and has every command.
- **`ops-gateway-ctl`** (`w17/services/ops-ctl/`, a module of its own that
  imports none of the project's code) connects only through the public gRPC
  surface, so it has only the commands whose methods are on it. `job count`
  (`CountJobs`) is not among them.

```sh
OPS_GATEWAY_GATEWAY_ADDR=localhost:8082 go run ./w17/services/ops-ctl/cmd/ops-gateway-ctl job get 710001
```

## Tests

The generated e2e suite drives REST, MCP and the admin, and this domain has
none of them, so the lock has `e2e: false`. `livetest/run.sh` brings the
stack up with the rows of `fixtures/ops/test.json` (seeded by the step in
`compose.rpc-cli.yaml`) and checks, over reflection and with grpcurl, which
services and methods the surface serves, calls `GetJob` through it, and
runs both binaries against the stack.

## Run it

```sh
w17ctl codegen
livetest/run.sh      # brings the stack up, runs the checks, tears it down
```

Or bring the stack up and leave it running:

```sh
w17ctl stack up --build ops-postgres   # the database, and its schema
w17ctl stack up --build --no-sync      # the rest, after the seed
```
