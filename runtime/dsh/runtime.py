"""Private group Agent worker. The volume owns instances and the execution journal."""
import contextlib
import fcntl
import hmac
import json
import os
import re
import secrets
import sqlite3
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

    def payload(self):
        size = int(self.headers.get("Content-Length", 0))
        if not 0 < size <= 4096:
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


class Runtime:
    def __init__(self, root, owner, callback, tls, model):
        self.root, self.owner, self.model = root, owner, model
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
                CREATE TABLE IF NOT EXISTS owners(agent TEXT PRIMARY KEY, epoch INTEGER NOT NULL);
            """)
        # One active DSH per logical instance. Other HTTP callers can query its journal.
        self.lock = threading.Lock()
        self.busy = set()

    @contextlib.contextmanager
    def db(self):
        with contextlib.closing(sqlite3.connect(self.journal, timeout=5)) as db, db:
            yield db

    def status(self, work):
        with self.db() as db:
            return db.execute("SELECT session,turn,completed FROM work WHERE id=?", (work,)).fetchone()

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
                    return 200, {"session": session, "turn": row[4]}
                db.execute("""INSERT INTO work(id,agent,session,epoch,generation) VALUES(?,?,?,?,?)
                           ON CONFLICT(id) DO UPDATE SET epoch=excluded.epoch,generation=excluded.generation""",
                           (job, agent, session, epoch, b["generation"]))
            turn = self.run_turn(b)
            with self.db() as db:
                db.execute("UPDATE work SET completed=1,turn=? WHERE id=?", (turn, job))
            return 200, {"session": session, "turn": turn}
        finally:
            with self.lock:
                self.busy.remove(agent)

    def run_turn(self, b):
        nonce = secrets.token_urlsafe(32)
        started = threading.Event()
        observed = {}
        runtime = self

        class LocalTool(Handler):
            def do_POST(self):
                if not hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + nonce):
                    return self.reply(403)
                try:
                    if self.payload() != {"session": b["session"]} or not started.wait(5):
                        return self.reply(403)
                    request = urllib.request.Request(runtime.gateway + "/internal/tools/context.whoami", json.dumps({**b, "turn": observed["turn"]}).encode(),
                                                     headers={"Content-Type": "application/json"})
                    with runtime.http.open(request, timeout=10) as resp:
                        data = json.load(resp)
                    observed["tool"] = True
                    return self.reply(200, data)
                except (ValueError, OSError):
                    return self.reply(403)

        def events(notification):
            if notification.payload.get("sessionId") != b["session"]:
                return
            event = notification.payload.get("event", {})
            data = event.get("data", {})
            if (event.get("type") == "turn/end" and observed.get("tool")
                    and data.get("turn") == observed.get("turn") and data.get("reason", {}).get("kind") == "completed"):
                # Persist before SDK shutdown or a lost HTTP acknowledgement.
                with self.db() as db:
                    db.execute("UPDATE work SET completed=1,turn=? WHERE id=? AND generation=?",
                               (observed["turn"], b["work"], b["generation"]))
            if event.get("type") == "turn/start":
                observed["turn"] = event["data"]["turn"]
                request = urllib.request.Request(self.gateway + "/internal/turns/start",
                                                 json.dumps({**b, "turn": observed["turn"]}).encode(),
                                                 headers={"Content-Type": "application/json"})
                with self.http.open(request, timeout=10):
                    started.set()

        with ThreadingHTTPServer(("127.0.0.1", 0), LocalTool) as bridge:
            threading.Thread(target=bridge.serve_forever, daemon=True).start()
            try:
                with open_instance(self.root / "agents", b["agent"], HERE / "project-pm.md",
                                   patches=(str(HERE / "group.patch.yml"),),
                                   tool_env={"OMNI_TOOL_URL": f"http://127.0.0.1:{bridge.server_port}",
                                             "OMNI_TOOL_NONCE": nonce}, **self.model) as host:
                    try:
                        session = host.resume_session(b["session"])
                    except JsonRpcError as exc:
                        if exc.message != f'session "{b["session"]}" not found':
                            raise
                        session = host.start_session(b["session"])
                    watchdog = threading.Timer(60, host.close)
                    watchdog.start()
                    try:
                        result = session.run("请用 context_whoami 核对本轮发起人和当前项目。",
                                             on_notification=events)
                    finally:
                        watchdog.cancel()
                        watchdog.join()
                if not observed.get("tool") or result.finish_reason != "completed":
                    raise RuntimeError("no completed tool-backed turn")
                return observed["turn"]
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
                b = self.payload()
                if (set(b) != {"work", "agent", "session", "owner", "epoch", "generation"}
                        or any(type(b[k]) is not int for k in ("epoch", "generation")) or b["work"] != job):
                    return self.reply(400)
                status, value = self.server.runtime.execute(b)
                return self.reply(status, value)
            row = self.server.runtime.status(job)
            if row and row[2]:
                return self.reply(200, {"session": row[0], "turn": row[1]})
            return self.reply(202 if row else 404)
        except (ValueError, TypeError, KeyError):
            return self.reply(400)
        except Exception as exc:
            print("Runtime work deferred:", type(exc).__name__, flush=True)
            return self.reply(503)

    def do_GET(self):
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
    model = dict(base_url=env["DEEPSEEK_BASE_URL"], api_key=env["DEEPSEEK_API_KEY"], model=env["DSH_MODEL"])
    runtime = Runtime(Path(env.get("AGENT_VOLUME", "/var/lib/omnirelay-agent")), env["AGENT_OWNER"],
                      env["AGENT_GATEWAY_URL"], client, model)
    with ThreadingHTTPServer((env.get("AGENT_LISTEN_HOST", "0.0.0.0"), int(env.get("AGENT_PORT", "8443"))), API) as app:
        app.runtime = runtime
        app.gateway_name = env["AGENT_GATEWAY_NAME"]
        app.socket = server.wrap_socket(app.socket, server_side=True)
        app.serve_forever()


if __name__ == "__main__":
    main()
