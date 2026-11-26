# syntax=docker/dockerfile:1

# Build a static binary. modernc.org/sqlite is pure Go, so CGO is not needed.
FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/fake-omise ./cmd/fake-omise \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="fake-omise" \
      org.opencontainers.image.description="Fake Omise API: 3DS once, then silent merchant-initiated card-on-file charges" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.source="https://github.com/akaporn-katip/fake-omise"

COPY --from=build /out/fake-omise /usr/local/bin/fake-omise
# Owned by the distroless nonroot user so a named volume mounted at /data is writable.
COPY --from=build --chown=65532:65532 /out/data /data

WORKDIR /data
USER 65532:65532

ENV FAKE_OMISE_DB=/data/fake-omise.db
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/fake-omise"]
CMD ["-addr", ":8080", "-db", "/data/fake-omise.db"]
