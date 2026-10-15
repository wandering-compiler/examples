FROM golang:1.26-alpine AS build
# The module proxy the CI job serves, when there is one (compose passes it
# through build.args); unset, go uses its default.
ARG GOPROXY
ARG GOSUMDB
ARG GODEBUG
WORKDIR /src
COPY . .
RUN cd register && CGO_ENABLED=0 GOWORK=off go build -buildvcs=false -o /out .

FROM alpine:3.22
COPY --from=build /out /usr/local/bin/register
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/register"]
