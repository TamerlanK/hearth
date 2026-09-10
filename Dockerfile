FROM --platform=$BUILDPLATFORM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
ARG VERSION=dev COMMIT=none DATE=unknown
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags "-s -w \
      -X github.com/TamerlanK/hearth/internal/cli.Version=$VERSION \
      -X github.com/TamerlanK/hearth/internal/cli.Commit=$COMMIT \
      -X github.com/TamerlanK/hearth/internal/cli.Date=$DATE" \
    -o /out/hearth ./cmd/hearth

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/hearth /hearth
EXPOSE 4000 9090
USER nonroot:nonroot
ENTRYPOINT ["/hearth"]
CMD ["serve", "--addr", ":4000", "--metrics-addr", ":9090"]
