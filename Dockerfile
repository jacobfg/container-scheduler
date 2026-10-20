# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27.1 AS build
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /container-scheduler ./cmd/container-scheduler
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /container-scheduler /container-scheduler
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 CMD ["/container-scheduler", "healthcheck"]
ENTRYPOINT ["/container-scheduler"]
