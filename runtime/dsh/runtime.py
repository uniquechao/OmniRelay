"""Private group Agent worker. The volume owns instances and the execution journal."""
import contextlib
import fcntl
import hashlib
import hmac
import json
import os
import re
import secrets
import signal
import sqlite3
import subprocess
import sys
import ssl
import threading
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from deepseek_harness.errors import JsonRpcError
from instance import open_instance

HERE = Path(__file__).resolve().parent
WORK = re.compile(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}")
HASH = re.compile("[0-9a-f]{64}")


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def payload(self, limit=4096):
        size = int(self.headers.get("Content-Length", 0))
        if not 0 < size <= limit:
            raise ValueError("invalid size")
        return json.loads(self.rfile.read(size))

    def reply(self, status, value=None):
        body = json.dumps(value or {}, ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args):
        return None


class ModelUnavailable(RuntimeError):
    pass


def model_config():
    path = os.environ.get("DEEPSEEK_API_KEY_FILE")
    try:
        key = Path(path).read_text(encoding="utf-8").strip() if path else os.environ.get("DEEPSEEK_API_KEY", "").strip()
    except FileNotFoundError:
        key = ""
    if not key:
        raise ModelUnavailable("model key pending")
    return dict(base_url=os.environ["DEEPSEEK_BASE_URL"], api_key=key, model=os.environ["DSH_MODEL"])


class Runtime:
    def __init__(self, root, owner, callback, tls):
        self.root, self.owner = root, owner
        if not callback.startswith("https://"):
            raise ValueError("private HTTPS callback required")
        self.gateway = callback.rstrip("/")
        self.http = urllib.request.build_opener(urllib.request.HTTPSHandler(context=tls), NoRedirect(),
                                                urllib.request.ProxyHandler({}))
        root.mkdir(mode=0o700, parents=True, exist_ok=True)
        self.volume_lock = (root / "owner.lock").open("a")
        fcntl.flock(self.volume_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        self.journal = root / "work.sqlite"
        with self.db() as db:
            db.executescript("""
                CREATE TABLE IF NOT EXISTS work(id TEXT PRIMARY KEY, agent TEXT, session TEXT, epoch INTEGER,
                    generation INTEGER, turn INTEGER, completed INTEGER NOT NULL DEFAULT 0);
                CREATE TABLE IF NOT EXISTS chat_answers(work_id TEXT PRIMARY KEY, answer TEXT NOT NULL);
                CREATE TABLE IF NOT EXISTS tool_commands(
                    work_id TEXT NOT NULL, signature TEXT NOT NULL, slot INTEGER NOT NULL,
                    pending TEXT, PRIMARY KEY(work_id,signature), UNIQUE(work_id,slot));
                CREATE TABLE IF NOT EXISTS owners(agent TEXT PRIMARY KEY, epoch INTEGER NOT NULL);
            """)
        # One active DSH per logical instance. Other HTTP callers can query its journal.
        self.lock = threading.Lock()
        self.busy = set()

    @contextlib.contextmanager
    def db(self):
        with contextlib.closing(sqlite3.connect(self.journal, timeout=5)) as db, db:
            yield db

    def watch_plans(self, stopping):
        clocks = {}
        def stop(clock):
            if clock.poll() is None:
                try:
                    os.killpg(clock.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
            clock.wait()
        try:
            while not stopping.is_set():
                clocks = {plan: proc for plan, proc in clocks.items() if proc.poll() is None}
                try:
                    with self.http.open(self.gateway + "/internal/schedules", timeout=5) as response:
                        pending = json.load(response) or []
                    if not isinstance(pending, list) or len(pending) > 4 or not all(WORK.fullmatch(p) for p in pending):
                        raise ValueError("invalid schedule assignment")
                    for plan in set(clocks) - set(pending):
                        stop(clocks.pop(plan))
                    for plan in pending:
                        if plan not in clocks:
                            clocks[plan] = subprocess.Popen([sys.executable, str(HERE / "notice_clock.py"), plan],
                                                            start_new_session=True)
                except (OSError, TypeError, ValueError):
                    pass  # Keep existing native timers alive during a Gateway outage.
                stopping.wait(2)
        finally:
            for proc in clocks.values():
                stop(proc)

    def status(self, work):
        with self.db() as db:
            return db.execute("SELECT session,turn,completed,answer FROM work LEFT JOIN chat_answers ON work_id=id WHERE id=?", (work,)).fetchone()

    def command_slot(self, work, tool, args):
        command = json.dumps({"tool": tool, "args": args}, sort_keys=True, separators=(",", ":"))
        signature = hashlib.sha256(command.encode()).hexdigest()
        with self.db() as db:
            db.execute("BEGIN IMMEDIATE")
            row = db.execute("SELECT slot FROM tool_commands WHERE work_id=? AND signature=?",
                             (work, signature)).fetchone()
            if row:
                return row[0]
            slot, = db.execute("SELECT coalesce(max(slot),0)+1 FROM tool_commands WHERE work_id=?", (work,)).fetchone()
            if slot > 128:
                raise ValueError("tool command limit")
            db.execute("INSERT INTO tool_commands VALUES(?,?,?,?)", (work, signature, slot, command))
            return slot

    def settled(self, work, slot):
        with self.db() as db:
            db.execute("UPDATE tool_commands SET pending=NULL WHERE work_id=? AND slot=?", (work, slot))

    def execute(self, b):
        agent, session, job = b["agent"], b["session"], b["work"]
        if (b["owner"] != self.owner or not HASH.fullmatch(agent) or not HASH.fullmatch(session)
                or not WORK.fullmatch(job) or min(b["epoch"], b["generation"]) < 1):
            return 403, {}
        with self.lock:
            if agent in self.busy:
                return 202, {}
            self.busy.add(agent)
        try:
            with self.db() as db:
                db.execute("BEGIN IMMEDIATE")
                row = db.execute("SELECT agent,session,epoch,generation,turn,completed FROM work WHERE id=?", (job,)).fetchone()
                if row and (row[:2] != (agent, session) or row[3] > b["generation"]):
                    return 409, {}
                current = db.execute("SELECT epoch FROM owners WHERE agent=?", (agent,)).fetchone()
                if current and current[0] > b["epoch"]:
                    return 409, {}
                epoch = b["epoch"]
                db.execute("INSERT INTO owners VALUES(?,?) ON CONFLICT(agent) DO UPDATE SET epoch=excluded.epoch",
                           (agent, epoch))
                if row and row[5]:
                    answer = db.execute("SELECT answer FROM chat_answers WHERE work_id=?", (job,)).fetchone()
                    return 200, {"session": session, "turn": row[4], **({"answer": answer[0]} if answer else {})}
                db.execute("""INSERT INTO work(id,agent,session,epoch,generation) VALUES(?,?,?,?,?)
                           ON CONFLICT(id) DO UPDATE SET epoch=excluded.epoch,generation=excluded.generation""",
                           (job, agent, session, epoch, b["generation"]))
            turn, answer = self.run_turn(b)
            with self.db() as db:
                if answer:
                    db.execute("INSERT INTO chat_answers VALUES(?,?) ON CONFLICT(work_id) DO NOTHING", (job, answer))
                db.execute("UPDATE work SET completed=1,turn=? WHERE id=?", (turn, job))
            return 200, {"session": session, "turn": turn, **({"answer": answer} if answer else {})}
        finally:
            with self.lock:
                self.busy.remove(agent)

    def run_turn(self, b):
        binding = {k: v for k, v in b.items() if k != "prompt"}
        chat = b.get("kind") in ("project_chat", "project_group_chat")
        nonce = secrets.token_urlsafe(32)
        started = threading.Event()
        observed = {}
        runtime = self
        catalog = []
        if chat:
            with self.http.open(self.gateway + "/internal/tools/project/catalog", timeout=10) as response:
                catalog = json.load(response)
        tools = {item["name"]: item for item in catalog}
        def project_request(tool, args, slot):
            payload = {**binding, "turn": observed["turn"], "tool": tool, "args": args, "slot": slot}
            request = urllib.request.Request(self.gateway + "/internal/tools/project", json.dumps(payload).encode(),
                                             headers={"Content-Type": "application/json"})
            try:
                with self.http.open(request, timeout=10) as response:
                    value = json.load(response)
            except urllib.error.HTTPError as exc:
                if slot and exc.code in (400, 403, 409):
                    self.settled(b["work"], slot)
                raise
            if slot:
                self.settled(b["work"], slot)
            return value

        class LocalTool(Handler):
            def do_POST(self):
                if not hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + nonce):
                    return self.reply(403)
                try:
                    call = self.payload(32 << 10)
                    if not isinstance(call, dict) or call.get("session") != b["session"] or not started.wait(5):
                        return self.reply(403)
                    data = {**binding, "turn": observed["turn"]}
                    path = "/internal/tools/context.whoami"
                    if set(call) != {"session"}:
                        if set(call) != {"session", "tool", "args"} or call["tool"] not in tools or not isinstance(call["args"], dict):
                            return self.reply(403)
                        name, args = call["tool"], call["args"]
                        slot = runtime.command_slot(b["work"], name, args) if tools[name]["write"] else 0
                        return self.reply(200, project_request(name, args, slot))
                    request = urllib.request.Request(runtime.gateway + path, json.dumps(data).encode(),
                                                     headers={"Content-Type": "application/json"})
                    with runtime.http.open(request, timeout=10) as resp:
                        data = json.load(resp)
                    observed["tool"] = True
                    return self.reply(200, data)
                except urllib.error.HTTPError as exc:
                    return self.reply(exc.code if exc.code in (400,403,409) else 503)
                except (TypeError, ValueError, OSError):
                    return self.reply(503)

        def events(notification):
            if notification.payload.get("sessionId") != b["session"]:
                return
            event = notification.payload.get("event", {})
            data = event.get("data", {})
            if (not chat and event.get("type") == "turn/end" and observed.get("tool")
                    and data.get("turn") == observed.get("turn") and data.get("reason", {}).get("kind") == "completed"):
                # Persist before SDK shutdown or a lost HTTP acknowledgement.
                with self.db() as db:
                    db.execute("UPDATE work SET completed=1,turn=? WHERE id=? AND generation=?",
                               (observed["turn"], b["work"], b["generation"]))
            if event.get("type") == "turn/start":
                observed["turn"] = event["data"]["turn"]
                request = urllib.request.Request(self.gateway + "/internal/turns/start",
                                                 json.dumps({**binding, "turn": observed["turn"]}).encode(),
                                                 headers={"Content-Type": "application/json"})
                with self.http.open(request, timeout=10):
                    pass
                if chat:
                    # Reconcile a lost acknowledgement before admitting new model writes.
                    with self.db() as db:
                        pending = db.execute("SELECT slot,pending FROM tool_commands WHERE work_id=? AND pending IS NOT NULL ORDER BY slot",
                                             (b["work"],)).fetchall()
                    for slot, raw in pending:
                        command = json.loads(raw)
                        try:
                            project_request(command["tool"], command["args"], slot)
                        except urllib.error.HTTPError as exc:
                            if exc.code not in (400, 403, 409):
                                raise
                started.set()
                if chat:
                    # Keep a current grant receipt even when a conversational turn needs no model tool.
                    scope = urllib.request.Request(self.gateway + "/internal/tools/context.whoami", request.data,
                                                   headers={"Content-Type": "application/json"})
                    with self.http.open(scope, timeout=10):
                        observed["tool"] = True

        with ThreadingHTTPServer(("127.0.0.1", 0), LocalTool) as bridge:
            threading.Thread(target=bridge.serve_forever, daemon=True).start()
            try:
                with open_instance(self.root / "agents", b["agent"], HERE / ("query.md" if b.get("kind") == "private_reply" else "project-pm.md"),
                                   patches=(str(HERE / "group.patch.yml"),),
                                   tool_env={"OMNI_TOOL_URL": f"http://127.0.0.1:{bridge.server_port}",
                                             "OMNI_PROJECT_TOOLS": json.dumps(catalog, ensure_ascii=False),
                                             "OMNI_TOOL_NONCE": nonce}, **model_config()) as host:
                    try:
                        session = host.resume_session(b["session"])
                    except JsonRpcError as exc:
                        if exc.message != f'session "{b["session"]}" not found':
                            raise
                        session = host.start_session(b["session"])
                    watchdog = threading.Timer(60, host.close)
                    watchdog.start()
                    try:
                        prompt = {"private_reply": "请用 context_whoami 核对我的账号。", "group_notice": "请用 context_whoami 获取群可见的项目提醒上下文。",
                                  "person_notice": "请用 context_whoami 获取本人的项目提醒上下文。"}.get(b.get("kind"), "请用 context_whoami 核对本轮发起人和当前项目。")
                        result = session.run(b["prompt"] if chat else prompt,
                                             on_notification=events)
                    finally:
                        watchdog.cancel()
                        watchdog.join()
                if not observed.get("tool") or result.finish_reason != "completed":
                    raise RuntimeError("no completed tool-backed turn")
                answer = result.final_response.strip()[:12000] if chat else ""
                if chat and not answer:
                    raise RuntimeError("empty chat response")
                return observed["turn"], answer
            finally:
                bridge.shutdown()


class API(Handler):
    def handle_work(self, post):
        if ("DNS", self.server.gateway_name) not in self.connection.getpeercert().get("subjectAltName", ()):
            return self.reply(403)
        prefix = "/works/"
        job = self.path.removeprefix(prefix)
        if not self.path.startswith(prefix) or not WORK.fullmatch(job):
            return self.reply(404)
        try:
            if post:
                b = self.payload(32 << 10)
                chat = b.get("kind") in ("project_chat", "project_group_chat")
                if (b.get("kind", "") not in ("", "private_reply", "group_notice", "person_notice", "project_chat", "project_group_chat")
                        or (set(b) - {"kind"}) != ({"work", "agent", "session", "owner", "epoch", "generation"} | ({"prompt"} if chat else set()))
                        or (chat and (not isinstance(b["prompt"], str) or not b["prompt"].strip()))
                        or any(type(b[k]) is not int for k in ("epoch", "generation")) or b["work"] != job):
                    return self.reply(400)
                status, value = self.server.runtime.execute(b)
                return self.reply(status, value)
            row = self.server.runtime.status(job)
            if row and row[2]:
                return self.reply(200, {"session": row[0], "turn": row[1], **({"answer": row[3]} if row[3] else {})})
            return self.reply(202 if row else 404)
        except (ValueError, TypeError, KeyError):
            return self.reply(400)
        except Exception as exc:
            print("Runtime work deferred:", type(exc).__name__, flush=True)
            return self.reply(503)

    def do_GET(self):
        if self.path in ("/livez", "/readyz"):
            names = self.connection.getpeercert().get("subjectAltName", ())
            if not any(("DNS", n) in names for n in (self.server.runtime.owner, self.server.gateway_name)):
                return self.reply(403)
            if self.path == "/readyz":
                try:
                    model_config()
                    with self.server.runtime.db() as db:
                        db.execute("SELECT 1 FROM work LIMIT 1").fetchone()
                except (ModelUnavailable, KeyError, OSError, sqlite3.Error):
                    return self.reply(503, {"ready": False})
            return self.reply(200)
        self.handle_work(False)

    def do_POST(self):
        self.handle_work(True)


def main():
    os.umask(0o077)
    env = os.environ
    client = ssl.create_default_context(cafile=env["AGENT_CA"])
    server = ssl.create_default_context(ssl.Purpose.CLIENT_AUTH, cafile=env["AGENT_CA"])
    for tls in (client, server):
        tls.minimum_version = ssl.TLSVersion.TLSv1_3
        tls.load_cert_chain(env["AGENT_CERT"], env["AGENT_KEY"])
    server.verify_mode = ssl.CERT_REQUIRED
    runtime = Runtime(Path(env.get("AGENT_VOLUME", "/var/lib/omnirelay-agent")), env["AGENT_OWNER"],
                      env["AGENT_GATEWAY_URL"], client)
    with ThreadingHTTPServer((env.get("AGENT_LISTEN_HOST", "0.0.0.0"), int(env.get("AGENT_PORT", "8443"))), API) as app:
        app.runtime = runtime
        app.gateway_name = env["AGENT_GATEWAY_NAME"]
        app.socket = server.wrap_socket(app.socket, server_side=True)
        stopping = threading.Event()
        watcher = threading.Thread(target=runtime.watch_plans, args=(stopping,), daemon=True)
        watcher.start()
        try:
            app.serve_forever()
        finally:
            stopping.set()
            watcher.join()


if __name__ == "__main__":
    main()
