FROM scratch
LABEL org.opencontainers.image.source="https://github.com/pgsty/silo"
COPY --chmod=0755 silo /usr/bin/silo
USER 1000:1000
ENTRYPOINT ["/usr/bin/silo"]
