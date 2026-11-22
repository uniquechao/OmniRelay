FROM python:3.12-slim-bookworm@sha256:935c3c5b12357a7e10caa9c1e6ca5692344d97cf229832ef584be2cec9fc03df
WORKDIR /opt/omnirelay-runtime
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 HOME=/var/lib/omnirelay-agent
COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt && python -c "from deepseek_harness import DeepSeekHarness" \
    && mkdir -p /var/lib/omnirelay-agent && chown 10001:10001 /var/lib/omnirelay-agent
COPY instance.py runtime.py whoami.cjs group.patch.yml project-pm.md query.md clock.cjs clock.patch.yml notice_clock.py ./
USER 10001:10001
VOLUME ["/var/lib/omnirelay-agent"]
EXPOSE 8443
ENTRYPOINT ["python", "/opt/omnirelay-runtime/runtime.py"]
