# Image for running scraperoutes -worker as a Cloud Run job. Build from the
# repository root (see run.sh):
#   docker buildx build --platform linux/amd64 -f cmd/scraperoutes/cloudrun/Dockerfile .
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/scraperoutes ./cmd/scraperoutes
RUN mkdir /out/empty

FROM gcr.io/distroless/static-debian12
WORKDIR /app
COPY --from=build /out/scraperoutes ./scraperoutes
COPY resources/ ./resources/
# util.findResourcesBasePath probes for a resources/videomaps directory; its
# contents (excluded via Dockerfile.dockerignore) are not needed by the worker.
COPY --from=build /out/empty ./resources/videomaps/
ENTRYPOINT ["/app/scraperoutes"]
