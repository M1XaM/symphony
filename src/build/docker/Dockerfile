# Builder image for Symphony. Contains everything `wails build` needs on
# Linux so the host only needs Docker and make:
#   Go (toolchain + module cache), Node + npm, gcc, pkg-config,
#   GTK3 / WebKit2GTK 4.1 headers, and the Wails CLI.
#
# Built by `make image`; used by `make build`. The source tree is bind-mounted
# at /work by the Makefile, nothing is copied in at image build time.
FROM golang:1.25-bookworm

ARG WAILS_VERSION=v2.15.0

RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential pkg-config ca-certificates \
      libgtk-3-dev libwebkit2gtk-4.1-dev \
    && rm -rf /var/lib/apt/lists/*

# Node + npm, lifted from the official image instead of a curl|bash installer.
COPY --from=node:22-bookworm-slim /usr/local/bin/node /usr/local/bin/node
COPY --from=node:22-bookworm-slim /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
 && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx

# Caches live under /cache so the Makefile can mount named volumes there and
# keep module downloads between builds.
ENV GOMODCACHE=/cache/go/mod \
    GOCACHE=/cache/go/build \
    npm_config_cache=/cache/npm \
    GOFLAGS=-buildvcs=false
RUN mkdir -p /cache/go/mod /cache/go/build /cache/npm

RUN go install github.com/wailsapp/wails/v2/cmd/wails@${WAILS_VERSION}

WORKDIR /work
CMD ["bash", "build/docker/build.sh"]
