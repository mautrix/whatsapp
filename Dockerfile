FROM golang:1-alpine AS builder

RUN apk add --no-cache git ca-certificates
COPY . /go/src/maunium.net/go/mautrix-whatsapp
WORKDIR /go/src/maunium.net/go/mautrix-whatsapp
RUN go get -u maunium.net/go/mautrix-whatsapp && go build -o /usr/bin/mautrix-whatsapp

FROM alpine:latest

COPY --from=builder /usr/bin/mautrix-whatsapp /usr/bin/mautrix-whatsapp
COPY --from=builder /etc/ssl/certs/ /etc/ssl/certs
COPY --from=builder /go/src/maunium.net/go/mautrix-whatsapp/example-config.yaml /opt/mautrix-whatsapp/example-config.yaml
COPY --from=builder /go/src/maunium.net/go/mautrix-whatsapp/docker-run.sh /docker-run.sh
VOLUME /data

CMD ["/docker-run.sh"]
