"""Exercise the pinned native SDK against an isolated model and bound tool callback."""
import io
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import tempfile
import threading
import unittest
import urllib.error
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import Mock, patch

from deepseek_harness.api import Session
from instance import legacy_personas
import runtime


class Model(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        packet = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.server.requests.append(packet)
        last = packet["messages"][-1]["content"]
        finished = isinstance(last, list) and any(x.get("type") == "tool_result" for x in last)
        name = next((t["name"] for t in packet["tools"] if t["name"] != "context_whoami"), "context_whoami")
        if finished:
            block = {"type": "text", "text": ""}
            delta = {"type": "text_delta", "text": "verified"}
        else:
            block = {"type": "tool_use", "name": name, "input": {}, "id": "test-" + uuid.uuid4().hex}
            delta = {"type": "input_json_delta", "partial_json": "{}"}
        usage = {"input_tokens": 1, "output_tokens": 1}
        events = [
            ("message_start", {"message": {"id":"test","type":"message","role":"assistant","model":"test",
                                           "content":[],"stop_reason":None,"usage":usage}}),
            ("content_block_start", {"index":0, "content_block":block}),
            ("content_block_delta", {"index":0, "delta":delta}),
            ("content_block_stop", {"index":0}),
            ("message_delta", {"delta":{"stop_reason":"end_turn" if finished else "tool_use"}, "usage":usage}),
            ("message_stop", {}),
        ]
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        for name, payload in events:
            data = json.dumps({"type": name, **payload})
            self.wfile.write(f"event: {name}\ndata: {data}\n\n".encode())


class Gateway:
    def __init__(self):
        self.calls = []

    def open(self, request, timeout):
        body = json.loads(request.data)
        path = request.full_url.split("https://test", 1)[1]
        self.calls.append((path, body))
        if path.endswith("/catalog"):
            name, write = {
                "project-pm": ("task_create", True), "query": ("file_get", False),
                "management-pm": ("visible_projects", False),
            }[body["role"]]
            result = [{"name":name, "write":write, "description":"Bound test tool",
                       "parameters":{"type":"object","properties":{},"additionalProperties":False}}]
        else:
            result = {"ok":True}
        return io.BytesIO(json.dumps(result).encode())


class RuntimeTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.root = Path(temp.name)
        model = ThreadingHTTPServer(("127.0.0.1", 0), Model)
        model.requests = []
        threading.Thread(target=model.serve_forever, daemon=True).start()
        self.addCleanup(model.server_close)
        self.addCleanup(model.shutdown)
        self.requests = model.requests
        folder = self.root / "app"
        folder.mkdir()
        for name in runtime.ROLE_TEMPLATE.values():
            shutil.copy(runtime.HERE / name, folder / name)
        content = (runtime.HERE / "group.patch.yml").read_text()
        (folder / "group.patch.yml").write_text(content.replace("/opt/omnirelay-runtime", str(runtime.HERE)))
        self.enterContext(patch.object(runtime, "HERE", folder))
        self.enterContext(patch.dict(os.environ, {
            "DEEPSEEK_API_KEY":"synthetic", "DSH_MODEL":"deepseek-v4-flash",
            "DEEPSEEK_BASE_URL":f"http://127.0.0.1:{model.server_port}/anthropic",
            "DEEPSEEK_API_KEY_FILE":"",
        }))
        self.gateway = Gateway()
        self.start_runtime()

    def start_runtime(self):
        self.worker = runtime.Runtime(self.root / "volume", "owner", "https://test", None)
        self.worker.http = self.gateway
        self.addCleanup(self.worker.volume_lock.close)

    def binding(self, role, prompt, version=0):
        return {"kind":"private_chat","work":str(uuid.uuid4()),"agent":uuid.uuid4().hex * 2,
                "session":uuid.uuid4().hex * 2,"generation":1,"epoch":1,"owner":"owner",
                "role":role,"persona":"tone-v" + str(version),"config_version":version,"prompt":prompt}

    def test_native_roles_config_and_replay(self):
        for role, tool in (("management-pm", "visible_projects"), ("project-pm", "task_create"), ("query", "file_get")):
            with self.subTest(role=role):
                binding = self.binding(role, "role-test-" + role)
                code, result = self.worker.execute(binding)
                self.assertEqual((code, result["answer"]), (200, "verified"))
                self.assertEqual(result["kind"], "writes" if role == "project-pm" else "answer")
                self.assertEqual(result["write_slots"], [1] if role == "project-pm" else [])
                self.assertEqual(result["applied_version"], 0)
                self.assertEqual({t["name"] for t in self.requests[-1]["tools"]}, {"context_whoami", tool})
                self.assertIn("tone-v0", str(self.requests[-1]["system"]))
                marker = (runtime.HERE / runtime.ROLE_TEMPLATE[role]).read_text().split("。")[0]
                self.assertIn(marker, str(self.requests[-1]["system"]))
                seen = len(self.requests)
                self.worker.volume_lock.close()
                self.start_runtime()
                self.assertEqual(self.worker.execute(dict(binding, generation=2)), (200, result))
                self.assertEqual(len(self.requests), seen)
        self.assertTrue(all("persona" not in body and "prompt" not in body for _, body in self.gateway.calls))
        update = dict(binding, work=str(uuid.uuid4()), config_version=1, persona="tone-v1")
        self.assertEqual(self.worker.execute(update)[1]["applied_version"], 1)
        self.assertIn("tone-v1", str(self.requests[-1]["system"]))
        self.assertNotIn("tone-v0", str(self.requests[-1]["system"]))
        self.assertEqual(self.worker.execute(dict(update, work=str(uuid.uuid4()), persona="different"))[0],422)
        self.assertEqual(self.worker.execute(dict(binding, work=str(uuid.uuid4())))[0],422)

    def test_query_cannot_load_write_tool(self):
        original = self.gateway.open
        def wrong_catalog(request, timeout):
            if request.full_url.endswith("/catalog"):
                return io.BytesIO(b'[{"name":"write","write":true}]')
            return original(request, timeout)
        self.gateway.open = wrong_catalog
        self.assertEqual(self.worker.execute(self.binding("query", "scope"))[0], 422)
        self.assertFalse(self.requests)

    def test_lost_reply_after_native_completion(self):
        binding = self.binding("project-pm", "Run once")
        real_run = Session.run
        def lose_ack(session, *args, **kwargs):
            real_run(session, *args, **kwargs)
            raise ConnectionError("native host terminated before returning")
        with patch.object(Session, "run", lose_ack), self.assertRaises(ConnectionError):
            self.worker.execute(binding)
        self.worker.volume_lock.close()
        self.start_runtime()
        before = len(self.requests)
        status, result = self.worker.execute(dict(binding, generation=2))
        self.assertEqual((status, result["answer"], result["write_slots"]), (200, "verified", [1]))
        self.assertEqual(self.worker.execute(dict(binding, prompt="changed"))[0], 409)
        self.assertEqual(len(self.requests), before)

    def test_lost_tool_ack_is_reconciled_before_caching(self):
        original = self.gateway.open
        slots = []
        def drop_first_ack(request, timeout):
            response = original(request, timeout)
            if request.full_url.endswith("/internal/tools/project"):
                body = json.loads(request.data)
                slots.append((body["work"], body["slot"], body["args"]))
                if len(slots) == 1:
                    raise ConnectionError("write acknowledgement lost")
            return response
        self.gateway.open = drop_first_ack
        binding = self.binding("project-pm", "durable write")
        code, result = self.worker.execute(binding)
        self.assertEqual((code, result["kind"], result["write_slots"]), (200, "writes", [1]))
        self.assertEqual(slots, [(binding["work"], 1, {}), (binding["work"], 1, {})])

    def test_explicit_write_rejection_survives_restart(self):
        original = self.gateway.open
        def reject_write(request, timeout):
            if request.full_url.endswith("/internal/tools/project"):
                raise urllib.error.HTTPError(request.full_url, 403, "forbidden", {}, None)
            return original(request, timeout)
        self.gateway.open = reject_write
        binding = self.binding("project-pm", "reject target")
        status, result = self.worker.execute(binding)
        self.assertEqual(status, 200)
        self.assertEqual(result["write_slots"], [])
        self.assertEqual(result["failed_writes"], [{"slot":1,"tool":"task_create","status":403}])
        self.worker.volume_lock.close()
        self.start_runtime()
        self.assertEqual(self.worker.status(binding["work"]), (status, result))

    def test_existing_volume_and_persona_inventory(self):
        volume = self.root / "old-volume"
        volume.mkdir()
        with sqlite3.connect(volume / "work.sqlite") as db:
            db.executescript("""
                CREATE TABLE work(id TEXT PRIMARY KEY,agent TEXT,session TEXT,epoch INTEGER,
                    generation INTEGER,turn INTEGER,completed INTEGER NOT NULL DEFAULT 0);
                CREATE TABLE chat_answers(work_id TEXT PRIMARY KEY,answer TEXT NOT NULL);
                CREATE TABLE tool_commands(work_id TEXT,signature TEXT,slot INTEGER,pending TEXT,
                    PRIMARY KEY(work_id,signature), UNIQUE(work_id,slot));
                INSERT INTO work VALUES('old','agent','original-session',1,4,9,1);
                INSERT INTO chat_answers VALUES('old','original answer');
                INSERT INTO tool_commands VALUES('old','old-signature',2,NULL);
            """)
        db.close()
        worker = runtime.Runtime(volume, "owner", "https://test", None)
        self.addCleanup(worker.volume_lock.close)
        status, result = worker.status("old")
        self.assertEqual((status, result["session"], result["turn"], result["answer"], result["applied_version"]),
                         (200, "original-session", 9, "original answer", 0))
        self.assertEqual(result["write_slots"], [])  # Legacy rows lack a successful/failed acknowledgement.
        self.assertEqual(result["kind"], "reconcile")
        self.assertEqual(worker.command_slot("old", "new", {}), 3)
        agent = volume / "agents" / ("a" * 64)
        agent.mkdir(parents=True)
        digest = hashlib.sha256(b"original").hexdigest()
        (agent / "origin.json").write_text(json.dumps({"template_sha256": digest, "agent_id":agent.name}))
        (agent / "persona.md").write_text("original")
        self.assertEqual(list(legacy_personas(agent.parent)), [])
        (agent / "persona.md").write_text("original hand edit")
        changed, = legacy_personas(agent.parent)
        self.assertEqual(changed["state"], "locally_modified")
        self.assertNotIn("original hand edit", str(changed))
        (agent / "origin.json").write_text("invalid-json")
        incomplete, = legacy_personas(agent.parent)
        self.assertEqual(incomplete, {"agent":agent.name, "state":"incomplete"})

    def test_five_simultaneous_schedules(self):
        plans = [str(uuid.uuid4()) for _ in range(5)]
        stopping = Mock()
        stopping.is_set.side_effect = [False, True]
        clock = Mock()
        clock.poll.return_value = 0
        self.worker.http = Mock()
        self.worker.http.open.return_value = io.BytesIO(json.dumps(plans).encode())
        with patch.object(runtime.subprocess, "Popen", return_value=clock) as start:
            self.worker.watch_plans(stopping)
        self.assertEqual([call.args[0][-1] for call in start.call_args_list], plans)


if __name__ == "__main__":
    unittest.main()
