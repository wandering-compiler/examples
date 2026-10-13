# The app domain's hand-written business server, built from the project's
# own Go module (the srcgo directory) and the clients codegen writes next to
# it. It hosts the facade services and reaches the generated storage
# tier over gRPC.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY srcgo/ .
RUN CGO_ENABLED=0 GOWORK=off go build -buildvcs=false -o /business ./domains/app/cmd/grpcapi-business

FROM alpine:3.22
RUN adduser -D -u 10001 business
COPY --from=build /business /usr/local/bin/business
# The domain's catalogs, so the facade's messages follow the caller's language.
COPY w17/languages/app /i18n
ENV APP_I18N_DIR=/i18n
USER business
ENTRYPOINT ["/usr/local/bin/business"]
