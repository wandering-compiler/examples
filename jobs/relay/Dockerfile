# The relay, built from the cluster plugin tree vendored into this project
# (proto/plugins/cluster). It is the same program an operator runs in
# production. The image also carries the setup the example needs before
# anything starts (see compose.jobs.yaml): the identities are made with
# `relay mint` and `relay issue-worker`.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY proto/plugins/cluster/src/ .
RUN CGO_ENABLED=0 go build -buildvcs=false -o /relay ./cmd/relay

FROM alpine:3.22
RUN adduser -D -u 10001 relay
COPY --from=build /relay /usr/local/bin/relay
COPY relay/setup.sh relay/serve.sh /usr/local/bin/
USER relay
ENTRYPOINT ["/usr/local/bin/serve.sh"]
