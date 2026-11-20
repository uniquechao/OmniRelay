FROM golang:1.26.11-alpine AS builder
ARG GO_MODULE_PROXY=https://proxy.golang.org
ARG GO_SUMDB=sum.golang.org
ENV GOPROXY=$GO_MODULE_PROXY GOSUMDB=$GO_SUMDB GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY design ./design
COPY internal ./internal
COPY migrations ./migrations
COPY web ./web
RUN CGO_ENABLED=0 go test -mod=readonly ./... && \
    CGO_ENABLED=0 go build -mod=readonly -trimpath -o /out/omnirelay ./cmd/omnirelay
FROM python:3.12-slim-bookworm
COPY internal/drive/preview/requirements.txt /opt/preview/requirements.txt
RUN pip install --no-cache-dir --only-binary=:all: -r /opt/preview/requirements.txt
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/omnirelay /omnirelay
USER 10000:10000
ENTRYPOINT ["/omnirelay"]
