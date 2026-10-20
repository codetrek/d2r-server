FROM golang:1.26.10-bookworm@sha256:ee3901c61e423732605a51f1b8157c849ec01194ea0a274213b5202b2c11e7bf AS source
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

FROM source AS test
RUN go test -race ./... && go vet ./...

FROM source AS build
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0
RUN GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" go build -trimpath -ldflags="-s -w" -o /out/d2r-server ./cmd/d2r-server \
    && GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" go build -trimpath -ldflags="-s -w" -o /out/d2r-inspect ./cmd/d2r-inspect \
    && GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" go build -trimpath -ldflags="-s -w" -o /out/d2r-trace ./cmd/d2r-trace

FROM scratch
USER 65532:65532
COPY --from=build /out/d2r-server /usr/local/bin/d2r-server
COPY --from=build /out/d2r-inspect /usr/local/bin/d2r-inspect
COPY --from=build /out/d2r-trace /usr/local/bin/d2r-trace
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/d2r-server"]
CMD ["-listen", "0.0.0.0:8080"]
