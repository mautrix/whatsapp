FROM golang:1-alpine3.19 AS builder

RUN apk add --no-cache git ca-certificates build-base su-exec olm-dev

COPY . /build
WORKDIR /build
ARG DBG=0
RUN <<EOF
if [ "$DBG" = 1 ]; then
    go install github.com/go-delve/delve/cmd/dlv@latest
else
    touch /go/bin/dlv
fi
EOF
RUN ./build.sh -o /usr/bin/mautrix-whatsapp

FROM alpine:3.19

ENV UID=1337 \
    GID=1337

RUN apk add --no-cache ffmpeg su-exec ca-certificates olm bash jq yq curl

COPY --from=builder /usr/bin/mautrix-whatsapp /usr/bin/mautrix-whatsapp
COPY --from=builder /build/example-config.yaml /opt/mautrix-whatsapp/example-config.yaml
COPY --from=builder /build/docker-run.sh /docker-run.sh
COPY --from=builder /go/bin/dlv /usr/bin/dlv
VOLUME /data

ARG DBG
ARG DBGWAIT=0
ENV DBG=${DBG} DBGWAIT=${DBGWAIT}
RUN echo "Debug mode: DBG=${DBG} DBGWAIT=${DBGWAIT}"
CMD ["/docker-run.sh"]
