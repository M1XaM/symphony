# One Dockerfile for every block; BLOCK picks the program. The build context
# is src/ (see src/.dockerignore), so the engine's go.mod is reused: the
# blocks import only the standard library, so nothing is downloaded.
#
#   docker build -f blocks/Dockerfile --build-arg BLOCK=service -t symphony/service:dev .
FROM golang:1.27-alpine AS build
ARG BLOCK
WORKDIR /src
COPY go.mod go.sum ./
COPY blocks ./blocks
ENV CGO_ENABLED=0 GOFLAGS=-mod=mod GOTOOLCHAIN=local
RUN go build -trimpath -ldflags="-s -w" -o /out/block ./blocks/${BLOCK}

FROM gcr.io/distroless/static:nonroot
ARG BLOCK
# the contract the block speaks (blocks/common/wire.Contract)
ARG CONTRACT=v0.2.0
# digest of the sources, set when the app builds the image itself; empty for
# an image built by hand, which the app then never replaces on its own
ARG SOURCE=
LABEL symphony.block="${BLOCK}" symphony.contract="${CONTRACT}" symphony.source="${SOURCE}"
COPY --from=build /out/block /block
EXPOSE 8080 9090
USER nonroot:nonroot
# distroless has no shell or curl: the block probes itself
HEALTHCHECK --interval=2s --timeout=2s --start-period=5s --retries=3 CMD ["/block", "-healthcheck"]
ENTRYPOINT ["/block"]
