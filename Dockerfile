# syntax=docker/dockerfile:1
FROM golang:1.26.9 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGET=relay
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/minitap-ai/minitest-connector/internal/version.Version=${VERSION}" \
    -o /out/app ./cmd/${TARGET}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER 65532:65532
ENTRYPOINT ["/app"]
