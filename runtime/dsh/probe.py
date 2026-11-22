"""Private in-container probes. Only the explicit model probe makes an inference."""
import http.client
import os
from pathlib import Path
import socket
import ssl
import sys
from tempfile import TemporaryDirectory


def check_listener(path):
    env = os.environ
    tls = ssl.create_default_context(cafile=env["AGENT_CA"])
    tls.load_cert_chain(env["AGENT_CERT"], env["AGENT_KEY"])
    with socket.create_connection(("127.0.0.1", int(env.get("AGENT_PORT", "8443"))), timeout=3) as sock:
        with tls.wrap_socket(sock, server_hostname=env["AGENT_OWNER"]) as conn:
            conn.sendall(f"GET /{path} HTTP/1.0\r\n\r\n".encode("ascii"))
            response = http.client.HTTPResponse(conn)
            response.begin()
            return response.status == 200


def check_model():
    from deepseek_harness import DeepSeekHarness
    from runtime import HERE, model_config

    with TemporaryDirectory() as folder:
        with DeepSeekHarness(profile="sdk-minimal", patches=(str(HERE / "probe.patch.yml"),),
                             cwd=folder, dsh_home=str(Path(folder) / "home"),
                             request_timeout_seconds=45, **model_config()) as host:
            result = host.start_session("dsh-model-check").run("Reply with exactly DSH_OK.")
            return result.finish_reason == "completed" and result.final_response.strip() == "DSH_OK"


if __name__ == "__main__":
    try:
        mode, = sys.argv[1:]
        if mode not in ("livez", "readyz", "model"):
            raise ValueError("unknown probe")
        ok = check_model() if mode == "model" else check_listener(mode)
    except Exception as exc:
        print("DSH probe failed:", type(exc).__name__, file=sys.stderr)
        sys.exit(1)
    print(f"{mode} {'ok' if ok else 'pending'}")
    sys.exit(0 if ok else 1)
