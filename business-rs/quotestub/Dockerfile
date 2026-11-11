# The stand-in for the third-party Quotes API (openapi.yaml), for the test
# stack only: the app's egress client calls it as it would the real one.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /quotestub .

FROM alpine:3.23
RUN adduser -D -u 10001 quotestub
USER quotestub
COPY --from=build /quotestub /usr/local/bin/quotestub
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/quotestub"]
