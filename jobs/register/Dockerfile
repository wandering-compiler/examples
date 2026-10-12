FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
RUN cd register && CGO_ENABLED=0 GOWORK=off go build -buildvcs=false -o /out .

FROM alpine:3.22
COPY --from=build /out /usr/local/bin/register
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/register"]
