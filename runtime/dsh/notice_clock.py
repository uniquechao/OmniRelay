"""Run one pre-authorized native Harness timer. No audience selectors enter this session."""
from datetime import datetime, timezone
import fcntl
import json
import os
from pathlib import Path
import ssl
import sys
import time
import urllib.error
import urllib.request
import uuid

from deepseek_harness import DeepSeekHarness
from deepseek_harness.errors import JsonRpcError
from runtime import HERE, NoRedirect, model_config


def run(plan, gateway, tls, root, model):
    if str(uuid.UUID(plan)) != plan or not gateway.startswith("https://"):
        raise ValueError("invalid coordinator binding")
    folder = root / "clocks" / plan
    folder.mkdir(parents=True, exist_ok=True, mode=0o700)
    http = urllib.request.build_opener(urllib.request.HTTPSHandler(context=tls), NoRedirect(),
                                       urllib.request.ProxyHandler({}))

    def call(action, **event):
        req = urllib.request.Request(gateway.rstrip("/") + "/internal/schedules/" + action,
                                     json.dumps({"plan":plan, **event}).encode(),
                                     headers={"Content-Type":"application/json"})
        with http.open(req, timeout=10) as response:
            return json.load(response)

    with (folder / "clock.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        binding = call("prepare")
        at, epoch = binding["at"], binding["epoch"]
        due = datetime.fromisoformat(at.replace("Z", "+00:00"))
        with DeepSeekHarness(profile="sdk-minimal", patches=(str(HERE / "clock.patch.yml"),),
                             cwd=str(folder), dsh_home=str(folder / "home"),
                             env={"OMNI_PLAN":plan, "OMNI_DUE":at, "DSH_SYSTEM_PROMPT":
                                  "Activate only the pre-authorized timer using activate_notice. Acknowledge scheduled reminders."},
                             **model) as host, host.client.subscribe_session_notifications(plan) as notifications:
            try:
                session = host.resume_session(plan)
            except JsonRpcError as exc:
                if exc.message != f'session "{plan}" not found':
                    raise
                session = host.start_session(plan)
            result = session.run("Activate the pre-authorized timer.")
            receipt = next(json.loads(e["data"]["message"]["content"][0]["text"])
                           for e in result.events if e["type"] == "tool/result" and not e["data"].get("isError") and not e["data"]["message"].get("isError"))
            native = receipt["native"]
            call("bind", native=native, at=at, epoch=epoch)
            observed = receipt["occurred"]
            # The native catalog's lastDelivery is the recovery proof after an interrupted callback.
            while not observed:
                remaining = (due - datetime.now(timezone.utc)).total_seconds() + 120
                if remaining <= 0:
                    raise TimeoutError("native reminder not observed")
                payload = notifications.next(timeout_seconds=remaining).payload
                if payload.get("sessionId") != plan:
                    continue
                event = payload.get("event", {})
                data = event.get("data", {})
                if event.get("type") != "user/message" or data.get("source", {}).get("kind") != "schedule":
                    continue
                for block in data["content"]:
                    if block.get("type") != "text" or "reminders_json: " not in block["text"]:
                        continue
                    for item in json.loads(block["text"].split("reminders_json: ", 1)[1]):
                        if item["schedule_id"] == native:
                            observed = item["occurrence_at"]
            if datetime.fromisoformat(observed.replace("Z", "+00:00")) != due:
                raise ValueError("occurrence differs from plan")
            for attempt in range(4):
                try:
                    call("occur", native=native, at=observed, epoch=epoch)
                    return
                except urllib.error.HTTPError as exc:
                    if exc.code < 500 or attempt == 3:
                        raise
                except OSError:
                    if attempt == 3:
                        raise
                time.sleep(2 ** attempt)


if __name__ == "__main__":
    env = os.environ
    os.umask(0o077)
    tls = ssl.create_default_context(cafile=env["AGENT_CA"])
    tls.load_cert_chain(env["AGENT_CERT"], env["AGENT_KEY"])
    tls.minimum_version = ssl.TLSVersion.TLSv1_3
    run(sys.argv[1], env["AGENT_GATEWAY_URL"], tls, Path(env.get("AGENT_VOLUME", "/var/lib/omnirelay-agent")),
        model_config())
