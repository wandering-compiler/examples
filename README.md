# w17 examples

Example projects built with [w17](https://github.com/wandering-compiler/w17ctl),
its [SDK](https://github.com/wandering-compiler/sdk) and its
[plugins](https://github.com/wandering-compiler/plugins). Each directory is a
complete w17 project: you write the protos (and any business logic), w17
generates the services, the database migrations, the API and the end-to-end
tests.

| example | what it shows |
|---|---|
| [`bookshop`](bookshop) | a catalog of authors and books behind a REST API; sign-up and sign-in with the `auth` plugin, public reads, validation and unique constraints, an end-to-end suite |
| [`assistant`](assistant) | notes and an assistant that answers questions about them with the `agent` plugin: a hand-written service over it, a tool the model calls, streaming, usage recording and limits, a TLS stand-in for the provider, an end-to-end suite and a live suite |
| [`billing`](billing) | card payments and a prepaid credit wallet with the `payment` plugin, the provider's signed webhooks, a stand-in for the Stripe API, an end-to-end suite and a live suite against the running stack |
| [`jobs`](jobs) | jobs on a pool of workers with the `cluster` plugin: a relay built from the plugin, two workers on its worker library, pinned identities set up before start, a hand-written service that places each job and calls the worker with a single-use ticket, an end-to-end suite and a live suite |

## Running an example

You need Docker and [`w17ctl`](https://github.com/wandering-compiler/w17ctl#install).

```sh
cd bookshop
w17ctl login            # once
w17ctl codegen          # generate the services from the protos
w17ctl test             # bring the stack up, run the e2e suite, tear it down
```

Generated code (`w17/services`, `w17/stubs`, `w17/specs`, …) is not committed —
`w17ctl codegen` writes it. What is committed is what you would write yourself:
the protos under `proto/`, hand-written code under `srcgo/`, the e2e cases under
`w17/e2e/`, and the lock (`w17/lock.yaml`).

## These examples are also the adoption test

The examples pin released versions of the SDK and the plugins. The SDK and the
plugins repositories run them, on every pull request, against the change under
review — so a change that breaks a project built on it fails before it is
merged. Each example runs in a job of its own, and an example with a live suite
(`livetest/run.sh`) runs it on the same stack as its end-to-end suite. The
reusable workflow is
[`.github/workflows/adoption.yml`](.github/workflows/adoption.yml):

```yaml
jobs:
  adoption:
    uses: wandering-compiler/examples/.github/workflows/adoption.yml@main
    with:
      sdk_ref: ${{ github.event.pull_request.head.sha }}   # or plugins_ref
    secrets: inherit
```

Every night the examples also run against the `main` branches of both.
