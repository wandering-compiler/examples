# The example's stand-in for the Stripe API; see main.go.
FROM golang:1.26-alpine AS build
# The module proxy the CI job serves, when there is one (compose passes it
# through build.args); unset, go uses its default.
ARG GOPROXY
ARG GOSUMDB
ARG GODEBUG
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /fakestripe .

FROM alpine:3.22
COPY --from=build /fakestripe /fakestripe
USER 65532:65532
EXPOSE 12111
HEALTHCHECK --interval=1s --timeout=2s --retries=30 CMD wget -qO- http://127.0.0.1:12111/healthz || exit 1
ENTRYPOINT ["/fakestripe"]
