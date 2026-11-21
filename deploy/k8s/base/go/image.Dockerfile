FROM node:22-bookworm-slim AS frontend
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY frontend ./frontend
COPY web ./web
COPY design/prototype ./design/prototype
COPY vite.config.mjs tsconfig.json ./
RUN npm run typecheck && npm run build

FROM golang:1.26.11-alpine AS builder
ARG GO_MODULE_PROXY=https://proxy.golang.org
ARG GO_SUMDB=sum.golang.org
ENV GOPROXY=$GO_MODULE_PROXY GOSUMDB=$GO_SUMDB GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY tests/fixtures ./tests/fixtures
COPY internal ./internal
COPY migrations ./migrations
COPY web ./web
COPY --from=frontend /src/web/dist ./web/dist
ARG OMNIRELAY_SOURCE_SHA
RUN CGO_ENABLED=0 go test -mod=readonly ./... && \
    CGO_ENABLED=0 go build -mod=readonly -trimpath -ldflags="-X main.receiverSourceSHA=${OMNIRELAY_SOURCE_SHA}" -o /out/omnirelay ./cmd/omnirelay
FROM python:3.12-slim-bookworm
ARG OMNIRELAY_SOURCE_SHA
LABEL io.omnirelay.ui.entry="react"
LABEL org.opencontainers.image.revision="${OMNIRELAY_SOURCE_SHA}"
COPY internal/drive/preview/requirements.txt /opt/preview/requirements.txt
ARG CANDIDATE_PIP_INDEX
RUN PIP_INDEX_URL="${CANDIDATE_PIP_INDEX:-${PIP_INDEX_URL:-https://pypi.org/simple}}" pip install --no-cache-dir --only-binary=:all: -r /opt/preview/requirements.txt
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/omnirelay /omnirelay
COPY --from=builder /src/web/dist /opt/omnirelay/ui
USER 10000:10000
ENTRYPOINT ["/omnirelay"]
