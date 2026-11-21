# Builds a tiny image: a static, stripped binary on an empty base image.
#
#   docker build -t wasdarfmeinticket .
#   docker build -t wasdarfmeinticket --build-arg COMPRESS=1 .   # smaller still, see below
#   docker run --rm -p 8080:8080 wasdarfmeinticket

FROM library/golang:1.26-alpine AS build
WORKDIR /src

# The module has no dependencies besides the standard library: go.mod only
# lists what the magefile needs, which is not part of the binary.
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal

# CGO_ENABLED=0 links statically, so the binary runs without a libc.
# -trimpath drops local paths, -s -w drop symbol table and debug information.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/wasdarfmeinticket ./cmd/wasdarfmeinticket

# COMPRESS=1 packs the binary with UPX to less than half its size. The price: it unpacks
# itself into memory at every start (slower start, more memory).
ARG COMPRESS=0
RUN if [ "$COMPRESS" = "1" ]; then apk add --no-cache upx && upx --best --lzma /out/wasdarfmeinticket; fi

# The cache directory, created here because the final image has no shell.
RUN mkdir /out/cache

FROM scratch
# The upstreams (bahn.de, Transitous, the Verbund planners) are HTTPS only.
# Time zone data is compiled into the binary and needs no files.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/wasdarfmeinticket /wasdarfmeinticket
COPY --from=build --chown=65532:65532 /out/cache /cache

# Run as an unprivileged user; scratch has no /etc/passwd, so by number.
USER 65532:65532
# Keeps upstream answers across restarts when /cache is a volume.
ENV WASDARFMEINTICKET_CACHE=/cache/cache.json
EXPOSE 8080
ENTRYPOINT ["/wasdarfmeinticket"]
CMD ["serve"]
