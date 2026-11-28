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
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from deepseek_harness.api import final_response
from deepseek_harness.errors import JsonRpcError
from instance import ConfigurationConflict, open_instance
from intent import semantic_plan

HERE = Path(__file__).resolve().parent
WORK = re.compile(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}")
HASH = re.compile("[0-9a-f]{64}")
ROLE_TEMPLATE = {"management-pm": "management-pm.md", "project-pm": "project-pm.md", "query": "query.md"}
CHAT_KINDS = {"private_chat", "project_group_chat", "project_chat"}
FIXED_PROMPTS = {
    "": "请用 context_whoami 核对本轮发起人和当前项目。",
    "private_reply": "请用 context_whoami 核对我的账号。",
    "group_notice": "请用 context_whoami 获取群可见的项目提醒上下文。",
    "person_notice": "请用 context_whoami 获取本人的项目提醒上下文。",
}


def valid_binding(b, job):
    if not isinstance(b, dict):
        return False
    kind = b.get("kind", "")
    chat = kind in CHAT_KINDS
    required = {"work", "agent", "session", "owner", "epoch", "generation", "role", "config_version"}
    if chat:
        required.add("prompt")
    return (required <= b.keys() <= required | {"kind", "persona", "execute_before_ms"}
            and (chat or kind in FIXED_PROMPTS) and b["work"] == job
            and b["role"] in ROLE_TEMPLATE
            and all(type(b[k]) is int and 0 < b[k] < 2**63 for k in ("epoch", "generation"))
            and type(b["config_version"]) is int and 0 <= b["config_version"] < 2**63
            and ("execute_before_ms" not in b or type(b["execute_before_ms"]) is int
                 and 0 < b["execute_before_ms"] < 2**53)
            and isinstance(b.get("persona", ""), str)
            and all(isinstance(b[k], str) for k in ("owner", "session", "agent"))
            and (not chat or isinstance(b["prompt"], str) and bool(b["prompt"].strip())))


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


def turn_deadline(binding):
    # A Gateway with the previous 100-second lease omits the deadline. Its
    # 85-second HTTP budget must also accommodate startup and host shutdown.
    before = binding.get("execute_before_ms")
    remaining = 60 if before is None else min(240, before / 1000 - time.time())
    if remaining <= 0:
        raise TimeoutError("native turn deadline reached")
    return time.monotonic() + remaining


def remaining_turn_time(deadline):
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise TimeoutError("native turn deadline reached")
    return remaining


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
                CREATE TABLE IF NOT EXISTS failed_writes(work_id TEXT NOT NULL, slot INTEGER NOT NULL,
                    tool TEXT NOT NULL, status INTEGER NOT NULL, PRIMARY KEY(work_id,slot));
            """)
            # Upgrade the original volume in place, retaining session IDs and command slots.
            db.execute("BEGIN IMMEDIATE")
            columns = {c[1] for c in db.execute("PRAGMA table_info(work)")}
            if "config_version" not in columns:
                db.execute("ALTER TABLE work ADD COLUMN config_version INTEGER NOT NULL DEFAULT 0")
            if "request_digest" not in columns:
                db.execute("ALTER TABLE work ADD COLUMN request_digest TEXT")
            if "requires_reconciliation" not in columns:
                db.execute("ALTER TABLE work ADD COLUMN requires_reconciliation INTEGER NOT NULL DEFAULT 0")
                db.execute("""UPDATE work SET requires_reconciliation=1
                              WHERE request_digest IS NULL AND EXISTS
                              (SELECT 1 FROM tool_commands WHERE work_id=work.id)""")
            if "succeeded" not in {c[1] for c in db.execute("PRAGMA table_info(tool_commands)")}:
                db.execute("ALTER TABLE tool_commands ADD COLUMN succeeded INTEGER NOT NULL DEFAULT 0")
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
                    if not isinstance(pending, list) or not all(isinstance(p, str) and WORK.fullmatch(p) for p in pending):
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
            row = db.execute("""SELECT session,turn,completed,answer,config_version,requires_reconciliation FROM work
                                LEFT JOIN chat_answers ON work_id=id WHERE id=?""", (work,)).fetchone()
            if not row or not row[2]:
                return (202 if row else 404), {}
            slots = [r[0] for r in db.execute("""SELECT slot FROM tool_commands
                         WHERE work_id=? AND succeeded=1 ORDER BY slot""", (work,))]
            failures = [{"slot": slot, "tool": tool, "status": status} for slot, tool, status in
                        db.execute("SELECT slot,tool,status FROM failed_writes WHERE work_id=? ORDER BY slot", (work,))]
            kind = "writes" if slots else "answer" if row[3] else "notice"
            return 200, {"session": row[0], "turn": row[1], "applied_version": row[4], "write_slots": slots,
                         "failed_writes": failures,
                         "kind": "reconcile" if row[5] else kind,
                         **({"answer": row[3]} if row[3] else {})}

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
            db.execute("INSERT INTO tool_commands(work_id,signature,slot,pending) VALUES(?,?,?,?)",
                       (work, signature, slot, command))
            return slot

    def settled(self, work, slot, *, rejected_tool=None, status=None):
        with self.db() as db:
            db.execute("""UPDATE tool_commands SET pending=NULL,succeeded=max(succeeded,?)
                          WHERE work_id=? AND slot=?""", (int(rejected_tool is None), work, slot))
            if rejected_tool is None:
                db.execute("DELETE FROM failed_writes WHERE work_id=? AND slot=?", (work, slot))
            else:
                db.execute("""INSERT INTO failed_writes(work_id,slot,tool,status)
                              SELECT work_id,slot,?,? FROM tool_commands WHERE work_id=? AND slot=? AND succeeded=0
                              ON CONFLICT(work_id,slot) DO NOTHING""",
                           (rejected_tool, status, work, slot))

    def complete(self, b, turn, answer):
        with self.db() as db:
            changed = db.execute("""UPDATE work SET completed=1,turn=? WHERE id=?
                                    AND completed=0 AND generation=?""", (turn, b["work"], b["generation"]))
            if changed.rowcount and answer:
                db.execute("INSERT INTO chat_answers(work_id,answer) VALUES(?,?)", (b["work"], answer))

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
            immutable = [b.get("kind", ""), b["role"], b.get("prompt", ""), b["config_version"], b.get("persona", "")]
            digest = hashlib.sha256(json.dumps(immutable, ensure_ascii=False).encode()).hexdigest()
            with self.db() as db:
                db.execute("BEGIN IMMEDIATE")
                row = db.execute("SELECT agent,session,epoch,generation,turn,completed,request_digest FROM work WHERE id=?", (job,)).fetchone()
                current = db.execute("SELECT epoch FROM owners WHERE agent=?", (agent,)).fetchone()
                if (row and row[3] > b["generation"]) or (current and current[0] > b["epoch"]):
                    return 409, {}
                if row and (row[:2] != (agent, session) or row[6] is not None and row[6] != digest):
                    return 409, {"error": "immutable_work_binding"}
                epoch = b["epoch"]
                db.execute("INSERT INTO owners VALUES(?,?) ON CONFLICT(agent) DO UPDATE SET epoch=excluded.epoch",
                           (agent, epoch))
                if row and row[5]:
                    return self.status(job)
                db.execute("""INSERT INTO work(id,agent,session,epoch,generation,config_version,request_digest)
                           VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET epoch=excluded.epoch,
                           generation=excluded.generation,config_version=excluded.config_version,
                           request_digest=excluded.request_digest""",
                           (job, agent, session, epoch, b["generation"], b["config_version"], digest))
            turn, answer = self.run_turn(b)
            self.complete(b, turn, answer)
            return self.status(job)
        except ConfigurationConflict:
            return 422, {}
        finally:
            with self.lock:
                self.busy.remove(agent)

    def run_turn(self, b):
        deadline = turn_deadline(b)
        binding = {k: v for k, v in b.items() if k not in ("prompt", "persona", "execute_before_ms")}
        chat = b.get("kind") in CHAT_KINDS
        nonce = secrets.token_urlsafe(32)
        started = threading.Event()
        observed = {}
        runtime = self
        catalog = []
        if chat:
            request = urllib.request.Request(self.gateway + "/internal/tools/project/catalog",
                                             json.dumps(binding).encode(), headers={"Content-Type": "application/json"})
            with self.http.open(request, timeout=min(10, remaining_turn_time(deadline))) as response:
                catalog = json.load(response) or []
            if b["role"] == "query" and any(item["write"] for item in catalog):
                raise ConfigurationConflict("write tool on a read-only role")
        tools = {item["name"]: item for item in catalog}
        def project_request(tool, args, slot):
            payload = {**binding, "turn": observed["turn"], "tool": tool, "args": args, "slot": slot}
            request = urllib.request.Request(self.gateway + "/internal/tools/project", json.dumps(payload).encode(),
                                             headers={"Content-Type": "application/json"})
            try:
                with self.http.open(request, timeout=min(10, remaining_turn_time(deadline))) as response:
                    value = json.load(response)
            except urllib.error.HTTPError as exc:
                if slot and exc.code in (400, 403, 409):
                    self.settled(b["work"], slot, rejected_tool=tool, status=exc.code)
                raise
            if slot:
                self.settled(b["work"], slot)
            return value

        def reconcile_pending():
            with self.db() as db:
                pending = db.execute("""SELECT slot,pending FROM tool_commands
                    WHERE work_id=? AND pending IS NOT NULL ORDER BY slot""", (b["work"],)).fetchall()
            for slot, raw in pending:
                command = json.loads(raw)
                try:
                    project_request(command["tool"], command["args"], slot)
                except urllib.error.HTTPError as exc:
                    if exc.code not in (400, 403, 409):
                        raise

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
                    with runtime.http.open(request, timeout=min(10, remaining_turn_time(deadline))) as resp:
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
            if event.get("type") == "assistant/message" and chat:
                observed["answer"] = final_response([event]).strip()[:12000]
            if (event.get("type") == "turn/end" and observed.get("tool")
                    and (not chat or observed.get("answer"))
                    and data.get("turn") == observed.get("turn") and data.get("reason", {}).get("kind") == "completed"):
                if chat:
                    # A model may finish after a lost write acknowledgement. Seal the
                    # real Gateway result before caching the turn for all future retries.
                    reconcile_pending()
                self.complete(b, observed["turn"], observed.get("answer", ""))
            if event.get("type") == "turn/start":
                observed["turn"] = event["data"]["turn"]
                request = urllib.request.Request(self.gateway + "/internal/turns/start",
                                                 json.dumps({**binding, "turn": observed["turn"]}).encode(),
                                                 headers={"Content-Type": "application/json"})
                with self.http.open(request, timeout=min(10, remaining_turn_time(deadline))):
                    pass
                if chat:
                    # Reconcile a lost acknowledgement before admitting new model writes.
                    reconcile_pending()
                if chat:
                    # Keep a current grant receipt even when a conversational turn needs no model tool.
                    scope = urllib.request.Request(self.gateway + "/internal/tools/context.whoami", request.data,
                                                   headers={"Content-Type": "application/json"})
                    with self.http.open(scope, timeout=min(10, remaining_turn_time(deadline))):
                        observed["tool"] = True
                started.set()

        with ThreadingHTTPServer(("127.0.0.1", 0), LocalTool) as bridge:
            threading.Thread(target=bridge.serve_forever, daemon=True).start()
            try:
                with open_instance(self.root / "agents", b["agent"], HERE / ROLE_TEMPLATE[b["role"]],
                                   role=b["role"], config_version=b["config_version"], persona=b.get("persona", ""),
                                   timeout_seconds=remaining_turn_time(deadline),
                                   patches=(str(HERE / "group.patch.yml"),),
                                   tool_env={"OMNI_TOOL_URL": f"http://127.0.0.1:{bridge.server_port}",
                                             "OMNI_PROJECT_TOOLS": json.dumps(catalog, ensure_ascii=False),
                                             "OMNI_TOOL_NONCE": nonce}, **model_config()) as host:
                    expired = threading.Event()
                    def close_expired_host():
                        expired.set()
                        host.close()
                    watchdog = threading.Timer(remaining_turn_time(deadline), close_expired_host)
                    watchdog.start()
                    try:
                        try:
                            session = host.resume_session(b["session"])
                        except JsonRpcError as exc:
                            if exc.message != f'session "{b["session"]}" not found':
                                raise
                            session = host.start_session(b["session"])
                        # Start the first block with application text so a user's leading slash
                        # stays model input rather than a native owner command.
                        prompt = "用户请求\n" + b["prompt"] if chat else FIXED_PROMPTS[b.get("kind", "")]
                        result = session.run(prompt, on_notification=events)
                    except Exception:
                        if expired.is_set():
                            raise TimeoutError("native turn deadline reached") from None
                        raise
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
                b = self.payload(128 << 10)
                if isinstance(b, dict) and (b.get("role") not in ROLE_TEMPLATE or not isinstance(b.get("persona", ""), str)
                                            or type(b.get("config_version")) is not int or b["config_version"] < 0):
                    return self.reply(422)
                if not valid_binding(b, job):
                    return self.reply(400)
                status, value = self.server.runtime.execute(b)
                return self.reply(status, value)
            return self.reply(*self.server.runtime.status(job))
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
        if self.path == "/intent":
            if ("DNS", self.server.gateway_name) not in self.connection.getpeercert().get("subjectAltName", ()):
                return self.reply(403)
            try:
                data = self.payload(128 << 10)
                return self.reply(200, semantic_plan(model_config(), data, NoRedirect()))
            except (ValueError, KeyError, TypeError, OSError, ModelUnavailable):
                return self.reply(503)
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
