FROM golang:1-alpine AS builder

RUN apk add --no-cache git ca-certificates build-base su-exec

WORKDIR /build
COPY go.mod go.sum /build/
RUN go get

COPY . /build
RUN go build -o /usr/bin/mautrix-whatsapp

FROM alpine:latest

ENV UID=1337 \
    GID=1337

RUN apk add --no-cache su-exec ca-certificates

COPY --from=builder /usr/bin/mautrix-whatsapp /usr/bin/mautrix-whatsapp
COPY --from=builder /build/example-config.yaml /opt/mautrix-whatsapp/example-config.yaml
COPY --from=builder /build/docker-run.sh /docker-run.sh
VOLUME /data

CMD ["/docker-run.sh"]
