FROM golang:1-alpine AS builder

RUN apk add --no-cache git ca-certificates build-base
RUN wget -qO /usr/local/bin/dep https://github.com/golang/dep/releases/download/v0.5.0/dep-linux-amd64
RUN chmod +x /usr/local/bin/dep

COPY Gopkg.lock Gopkg.toml /go/src/maunium.net/go/mautrix-whatsapp/
WORKDIR /go/src/maunium.net/go/mautrix-whatsapp
RUN dep ensure -vendor-only

COPY . /go/src/maunium.net/go/mautrix-whatsapp
RUN go build -o /usr/bin/mautrix-whatsapp

FROM alpine:latest

ENV UID=1337 \
    GID=1337

RUN apk add --no-cache \
      su-exec

COPY --from=builder /usr/bin/mautrix-whatsapp /usr/bin/mautrix-whatsapp
COPY --from=builder /etc/ssl/certs/ /etc/ssl/certs
COPY --from=builder /go/src/maunium.net/go/mautrix-whatsapp/example-config.yaml /opt/mautrix-whatsapp/example-config.yaml
COPY --from=builder /go/src/maunium.net/go/mautrix-whatsapp/docker-run.sh /docker-run.sh
VOLUME /data

CMD ["/docker-run.sh"]
