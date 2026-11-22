"""Deterministic Messages endpoint for the internal gate. Only synthetic data."""
import json
import sys
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Model(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        req = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        tools = {tool["name"] for tool in req.get("tools", [])}
        if tools not in ({"context_whoami"}, {"activate_notice"}):
            print("model-visible tools", sorted(tools), flush=True)
            self.send_error(400, "Unexpected tool roster")
            return
        last = req["messages"][-1]["content"]
        finished = isinstance(last, list) and any(x.get("type") == "tool_result" for x in last)
        history = str(req["messages"])
        if "群可见的项目提醒" in history and ("PRIVATE_ONLY_GATE" in history or "user_id" in history):
            self.send_error(400,"private field in group context")
            return
        if tools == {"activate_notice"} and "OMNI_NOTICE_DUE" in json.dumps(last):
            finished = True
        if finished:
            if isinstance(last, list) and any(x.get("is_error") for x in last): print("synthetic tool error", last, flush=True)
            block, delta, reason = {"type":"text","text":""}, {"type":"text_delta","text":"已核对"}, "end_turn"
        else:
            block = {"type":"tool_use","id":"call_"+uuid.uuid4().hex,"name":next(iter(tools)),"input":{}}
            delta, reason = {"type":"input_json_delta","partial_json":"{}"}, "tool_use"
        usage = {"input_tokens":10,"output_tokens":3}
        events = [
            ("message_start", {"message":{"id":"gate","type":"message","role":"assistant","model":req["model"],"content":[],"stop_reason":None,"usage":usage}}),
            ("content_block_start", {"index":0,"content_block":block}),
            ("content_block_delta", {"index":0,"delta":delta}),
            ("content_block_stop", {"index":0}),
            ("message_delta", {"delta":{"stop_reason":reason,"stop_sequence":None},"usage":usage}),
            ("message_stop", {}),
        ]
        self.send_response(200)
        self.send_header("Content-Type","text/event-stream")
        self.end_headers()
        for name, data in events:
            self.wfile.write(("event: "+name+"\ndata: "+json.dumps({"type":name,**data})+"\n\n").encode())


ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), Model).serve_forever()
