# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /app
RUN npm install --global pnpm@11.25.0
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY web/package.json web/package.json
RUN pnpm install --frozen-lockfile
COPY web web
COPY scripts/embed.mjs scripts/embed.mjs
RUN pnpm build:web

FROM --platform=$BUILDPLATFORM golang:1.26.10-alpine AS server
WORKDIR /app/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server .
COPY --from=web /app/server/internal/assets/dist internal/assets/dist
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/garden ./cmd/garden
RUN mkdir -p /empty/data /empty/backups && chmod 0700 /empty/data /empty/backups

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=server /out/garden /app/garden
COPY --from=server --chown=65532:65532 /empty/ /
USER 65532:65532
WORKDIR /app
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=3s --start-period=15s --retries=3 CMD ["/app/garden", "healthcheck"]
ENTRYPOINT ["/app/garden"]
CMD ["serve"]
