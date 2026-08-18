ARG BIN_NAME=papercast
ARG BIN_VERSION=<unknown>

FROM golang:1-trixie AS builder
ARG BIN_NAME
ARG BIN_VERSION

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-X main.version=${BIN_VERSION}" -o ./out/${BIN_NAME} .

FROM debian:trixie-slim
ARG BIN_NAME
ARG BIN_VERSION

RUN apt-get update && \
    apt-get install -y --no-install-recommends ffmpeg ca-certificates && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /src/out/${BIN_NAME} /usr/bin/${BIN_NAME}

ENTRYPOINT ["/usr/bin/papercast"]

LABEL license="MIT"
LABEL org.opencontainers.image.licenses="MIT"
LABEL maintainer="Chris Dzombak <https://www.dzombak.com>"
LABEL org.opencontainers.image.authors="Chris Dzombak <https://www.dzombak.com>"
LABEL org.opencontainers.image.url="https://github.com/cdzombak/papercast"
LABEL org.opencontainers.image.documentation="https://github.com/cdzombak/papercast"
LABEL org.opencontainers.image.source="https://github.com/cdzombak/papercast"
LABEL org.opencontainers.image.version="${BIN_VERSION}"
LABEL org.opencontainers.image.title="${BIN_NAME}"
LABEL org.opencontainers.image.description="Turn unread Instapaper articles into a podcast feed using Google Chirp 3 HD text-to-speech"
