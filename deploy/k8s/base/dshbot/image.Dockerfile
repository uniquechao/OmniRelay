FROM golang:1.26.11-alpine AS builder
WORKDIR /src
COPY cmd/dshbot/main.go .
RUN CGO_ENABLED=0 go build -trimpath -o /out/dshbot main.go

FROM python:3.12-slim-bookworm
COPY runtime/dsh/requirements.txt /opt/dshbot/requirements.txt
RUN pip install --no-cache-dir -r /opt/dshbot/requirements.txt
COPY runtime/dsh/console-sessions.cjs runtime/dsh/console-workspaces.cjs runtime/dsh/console-agents.cjs /opt/omnirelay-console/
COPY --from=builder /out/dshbot /usr/local/bin/dshbot
ARG OMNIRELAY_SOURCE_SHA
LABEL org.opencontainers.image.revision=$OMNIRELAY_SOURCE_SHA
USER 10001:10000
ENTRYPOINT ["dshbot"]
