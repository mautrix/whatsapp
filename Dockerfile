FROM golang:1-alpine AS builder

RUN echo "@edge_community http://dl-cdn.alpinelinux.org/alpine/edge/community" >> /etc/apk/repositories
RUN apk add --no-cache git ca-certificates build-base su-exec olm-dev@edge_community

WORKDIR /build
COPY go.mod go.sum /build/
RUN go get

COPY . /build
RUN go build -o /usr/bin/mautrix-whatsapp

FROM alpine:latest

ENV UID=1337 \
    GID=1337

RUN echo "@edge_community http://dl-cdn.alpinelinux.org/alpine/edge/community" >> /etc/apk/repositories
RUN apk add --no-cache su-exec ca-certificates olm@edge_community

COPY --from=builder /usr/bin/mautrix-whatsapp /usr/bin/mautrix-whatsapp
COPY --from=builder /build/example-config.yaml /opt/mautrix-whatsapp/example-config.yaml
COPY --from=builder /build/docker-run.sh /docker-run.sh
VOLUME /data

CMD ["/docker-run.sh"]
