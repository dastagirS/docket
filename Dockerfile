FROM node:24-alpine@sha256:06d7adf3e0ef61a13f1d28682726366f97a15aa1888c934d3ecec86c33918b53 AS frontend
WORKDIR /frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npm run build

# The builder must not inherit the host's optimized libc or CPU requirements.
FROM golang:1.27.3-alpine3.24@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS toolchain
RUN apk upgrade --no-cache && apk add --no-cache gcc=15.2.0-r5 musl=1.2.6-r3 musl-dev=1.2.6-r3 binutils=2.45.1-r1
ENV CGO_ENABLED=1 GOOS=linux GOARCH=amd64 GOAMD64=v1 GOTOOLCHAIN=local GOMAXPROCS=2
ENV CGO_CFLAGS="-O2 -march=x86-64 -mtune=generic"
WORKDIR /source
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd/ cmd/
COPY internal/ internal/
COPY --from=frontend /internal/docket/web/ internal/docket/web/

FROM toolchain AS test
RUN go test -race -count=1 -timeout=90s ./... && go vet ./...

FROM toolchain AS build
RUN go build -trimpath -tags 'netgo osusergo sqlite_omit_load_extension' -ldflags '-s -w -linkmode external -extldflags "-static"' -o /build/docket ./cmd/docket

FROM build AS security
RUN GOBIN=/audit go install golang.org/x/vuln/cmd/govulncheck@v1.9.0 \
    && /audit/govulncheck -C cmd/docket -scan=module \
    && /audit/govulncheck -scan=package -test -tags 'netgo,osusergo,sqlite_omit_load_extension' ./... \
    && /audit/govulncheck -mode=binary /build/docket

FROM scratch AS artifact
COPY --from=build /build/docket /docket

# QEMU is a verification tool only; it is not copied into the runtime image.
FROM build AS portable-test
RUN apk add --no-cache qemu-x86_64=11.0.3-r0
RUN qemu-x86_64 -cpu qemu64 /build/docket --database /tmp/compat.sqlite --backup-to /tmp/compat-backup.sqlite \
    && go test -exec 'qemu-x86_64 -cpu qemu64' -count=1 -timeout=90s ./...

FROM scratch AS runtime
COPY --chown=65532:65532 docker/data/ /data/
COPY --from=build /build/docket /docket
USER 65532:65532
VOLUME ["/data"]
EXPOSE 7331
ENTRYPOINT ["/docket"]
CMD ["--listen", "0.0.0.0:7331", "--database", "/data/docket.sqlite", "--public-url", "http://127.0.0.1:7331"]
