# Image for running wxingest as a Cloud Run job. Build from the repository
# root (see run.sh):
#   docker buildx build --platform linux/amd64 -f cmd/wxingest/cloudrun/Dockerfile .
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/wxingest ./cmd/wxingest
RUN mkdir /out/empty

FROM gcr.io/distroless/static-debian12
WORKDIR /app
COPY --from=build /out/wxingest ./wxingest
COPY resources/ ./resources/
# util.findResourcesBasePath probes for a resources/videomaps directory; its
# contents (excluded via Dockerfile.dockerignore) are not needed by wxingest.
COPY --from=build /out/empty ./resources/videomaps/
ENTRYPOINT ["/app/wxingest"]
