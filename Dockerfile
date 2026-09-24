ARG POSTGRES_IMAGE
ARG GO_IMAGE
ARG NODE_IMAGE
FROM ${NODE_IMAGE} AS frontend
WORKDIR /src/web
RUN npm install --global pnpm@10.17.1
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile --ignore-scripts
COPY web/ ./
RUN pnpm build

FROM ${GO_IMAGE} AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY web/embed.go web/embed.go
COPY --from=frontend /src/web/dist web/dist
ARG VERSION=dev
ARG COMMIT=unknown
ARG GO_TAGS=
RUN CGO_ENABLED=0 go build -tags "${GO_TAGS}" -trimpath -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" -o /out/pgfy ./cmd/pgfy

FROM ${POSTGRES_IMAGE}
# The base image ships no CA bundle; backups need to verify object-storage TLS.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 pgfy && useradd --uid 10001 --gid 10001 --no-create-home --shell /usr/sbin/nologin pgfy \
    && mkdir -p /data && chown 10001:10001 /data \
    && psql --version | grep -E ' 18\.[0-9]+([[:space:]]|$)' \
    && pg_dump --version | grep -E ' 18\.[0-9]+([[:space:]]|$)' \
    && pg_restore --version | grep -E ' 18\.[0-9]+([[:space:]]|$)'
COPY --from=backend /out/pgfy /usr/local/bin/pgfy
USER 10001:10001
WORKDIR /data
ENTRYPOINT ["/usr/local/bin/pgfy"]
CMD ["serve"]
EXPOSE 3000
