FROM golang:1-alpine3.12 AS builder

RUN apk add --no-cache git ca-certificates build-base su-exec olm-dev

COPY . /build
WORKDIR /build
RUN go build -o /usr/bin/mautrix-whatsapp

FROM alpine:3.12

ARG TARGETARCH=amd64
ARG YQ_DOWNLOAD_ADDR=https://github.com/mikefarah/yq/releases/download/3.3.2/yq_linux_${TARGETARCH}

ENV UID=1337 \
    GID=1337

RUN apk add --no-cache ffmpeg su-exec ca-certificates olm bash jq curl && \
    curl -sLo yq ${YQ_DOWNLOAD_ADDR} && \
    chmod +x yq && mv yq /usr/bin/yq

COPY --from=builder /usr/bin/mautrix-whatsapp /usr/bin/mautrix-whatsapp
COPY --from=builder /build/example-config.yaml /opt/mautrix-whatsapp/example-config.yaml
COPY --from=builder /build/docker-run.sh /docker-run.sh
VOLUME /data

CMD ["/docker-run.sh"]
