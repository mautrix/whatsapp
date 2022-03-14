###
### Compiling the Application
###

FROM golang:1-alpine3.15 AS builder

# install the dependencies to compile the application
RUN apk add --no-cache git ca-certificates build-base su-exec olm-dev

COPY . /build
WORKDIR /build
# compile the application
RUN go build -o /usr/bin/mautrix-whatsapp

###
###  Prepare the application for launch
###

FROM alpine:3.15

ENV UID=1337 \
    GID=1337
	
# install the dependencies
RUN apk add --no-cache ffmpeg su-exec ca-certificates olm bash jq yq curl

COPY --from=builder /usr/bin/mautrix-whatsapp /usr/bin/mautrix-whatsapp

# Import the example configuration
COPY --from=builder /build/example-config.yaml /opt/mautrix-whatsapp/example-config.yaml
COPY --from=builder /build/docker-run.sh /docker-run.sh

# In /data are user generated files stored
VOLUME /data

CMD ["/docker-run.sh"]
