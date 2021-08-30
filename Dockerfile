FROM golang:1-alpine3.14 AS builder

RUN apk add --no-cache git ca-certificates build-base su-exec olm-dev

COPY . /build
WORKDIR /build
RUN go build -o /usr/bin/chatwoot

FROM alpine:3.14

ENV UID=1337 \
    GID=1337

RUN apk add --no-cache su-exec ca-certificates olm bash

COPY --from=builder /usr/bin/chatwoot /usr/bin/chatwoot
COPY --from=builder /build/config.sample.json /opt/chatwoot/config.sample.json
COPY --from=builder /build/docker-run.sh /docker-run.sh
VOLUME /data

CMD ["/docker-run.sh"]
