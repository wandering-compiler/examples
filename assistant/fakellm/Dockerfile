# The example's stand-in for the language-model provider; see main.go.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /fakellm .

FROM alpine:3.22
COPY --from=build /fakellm /fakellm
# Where the certificate authority is written for the stack to trust; a
# volume mounted here starts with this ownership.
RUN mkdir /ca && chown 65532:65532 /ca
USER 65532:65532
EXPOSE 8443
HEALTHCHECK --interval=1s --timeout=2s --retries=30 CMD test -s /ca/ca.pem
ENTRYPOINT ["/fakellm"]
