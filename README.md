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
| [`billing`](billing) | card payments and a prepaid credit wallet with the `payment` plugin, the provider's signed webhooks, a stand-in for the Stripe API, an end-to-end suite and a live suite against the running stack |

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
merged. The reusable workflow is
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
