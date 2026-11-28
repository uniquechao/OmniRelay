"""Deterministic Messages endpoint for the internal gate. Only synthetic data."""
import json
import sys
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


PROJECT_TOOLS = set("""
context_whoami project_get project_update member_list member_add member_set_role member_update
member_candidates member_remove chat_list chat_search_messages chat_bind chat_unbind chat_set_primary
file_search file_get meeting_mark task_search task_get task_create task_update task_delete
task_transition task_add_progress task_board_get task_query task_move task_bulk_update task_board_save
task_tag_create task_tag_update task_tag_delete task_activity_list task_evidence_list task_feedback_list
task_feedback_submit task_feedback_review task_candidate_sources task_candidate_chat_sources
task_candidate_submit task_candidate_list task_candidate_review task_actions task_milestone_list
task_milestone_create task_evidence_link task_source_tasks task_business_time task_board_timezone
briefing_list briefing_get briefing_save daily_report_query
""".split())

class Model(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        req = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        output = req.get("tool_choice", {}).get("name") if isinstance(req.get("tool_choice"), dict) else None
        if output in {"classify_request", "report_plan"}:
            declared = req.get("tools", [])
            if (len(declared) != 1 or declared[0]["name"] != output or len(req["messages"]) != 1
                    or req.get("thinking") != {"type":"disabled"} or req.get("temperature") != 0):
                return self.send_error(400, "non-isolated or unstructured planner")
            original = req["messages"][0]["content"]
            if output == "classify_request":
                write_fixtures = ("ROLE_DENIED_GATE", "ROLE_DELEGATE_GATE", "ROLE_RECEIPT_GATE", "PM_GATE_CREATE", "ROLE_PARTIAL_GATE")
                route = "admin" if any(mark in original for mark in ("ROLE_LIST_GATE","ROLE_PROGRESS_GATE","ROLE_DELEGATE_GATE","ROLE_DENIED_GATE")) else "project"
                value = {"dialogue_act":"other","kind":"apply_business_change_now" if any(v in original for v in write_fixtures) else "inspect_existing_or_answer",
                         "intent":route, "evidence_period":None}
            else:
                source = json.loads(original)
                if set(source) - {"text","intent","private","displayed_proposal","project_selected"}:
                    return self.send_error(400,"unexpected planner context")
                text, intent = source["text"], source.get("intent","project")
                value = {"mode":"read","actions":[]}
                def action(tool, **fields):
                    value["mode"]="execute"
                    value["actions"].append({"tool":tool,"evidence":text,"fields":fields})
                if "ROLE_DENIED_GATE" in text:
                    action("delegate_project",project_id="locked")
                elif "ROLE_DELEGATE_GATE" in text:
                    if intent=="admin":
                        action("delegate_project",project_id="北辰项目")
                        value["actions"][0]["child_actions"]=[{"tool":"task_create","evidence":"创建任务 集成角色任务","fields":{"title":"集成角色任务"}}]
                    else:
                        action("task_create",title="集成角色任务")
                elif "ROLE_RECEIPT_GATE" in text:
                    action("task_create",title="ROLE_RECEIPT_GATE")
                elif "PM_GATE_CREATE" in text:
                    action("task_create",title="组内创建的验收任务")
                elif "ROLE_PARTIAL_GATE" in text:
                    action("task_create",title="已办事项")
                    action("task_create",title="待澄清事项",owner_member_id="ROLE_MISSING_MEMBER")
            body=json.dumps({"stop_reason":"tool_use","content":[{"type":"tool_use","id":"planned","name":output,"input":value}]}).encode()
            self.send_response(200)
            self.send_header("Content-Type","application/json")
            self.send_header("Content-Length",str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        tools = {tool["name"] for tool in req.get("tools", [])}
        smoke = not tools and "Reply with exactly DSH_OK." in str(req["messages"][-1])
        if smoke:
            print("DSH model probe path", self.path, flush=True)
        elif tools != {"activate_notice"} and (
            "context_whoami" not in tools or not tools <= PROJECT_TOOLS | {"visible_projects", "delegate_project", "delegation_status"}
        ):
            print("model-visible tools", sorted(tools), flush=True)
            self.send_error(400, "Unexpected tool roster")
            return
        last = req["messages"][-1]["content"]
        finished = isinstance(last, list) and any(x.get("type") == "tool_result" for x in last)
        question = ""
        recent = []
        for message in reversed(req["messages"]):
            if message.get("role") != "user":
                recent.append(message)
                continue
            content = message["content"]
            if isinstance(content, list) and any(x.get("type") == "tool_result" for x in content):
                continue
            question = str(content).rsplit("当前用户提问：",1)[-1]
            break
        source_test = "ROLE_QA_GATE" in question
        list_test = "ROLE_LIST_GATE" in question
        delegate_test = "ROLE_DELEGATE_GATE" in question
        denied_test = "ROLE_DENIED_GATE" in question
        fabricated_test = "ROLE_RECEIPT_GATE" in question
        progress_test = "ROLE_PROGRESS_GATE" in question
        title_only_test = "ROLE_TITLE_GATE" in question
        partial_test = "ROLE_PARTIAL_GATE" in question
        tool_uses = sum(1 for message in recent for item in message.get("content", [])
                        if isinstance(item, dict) and item.get("type") == "tool_use")
        if partial_test and tool_uses == 1:
            finished = False
        history = str(req["messages"])
        if "群可见的项目提醒" in history and ("PRIVATE_ONLY_GATE" in history or "user_id" in history):
            self.send_error(400,"private field in group context")
            return
        if tools == {"activate_notice"} and "OMNI_NOTICE_DUE" in json.dumps(last):
            finished = True
        if finished or smoke:
            if isinstance(last, list) and any(x.get("is_error") for x in last) and not (denied_test or partial_test):
                self.send_error(400, "synthetic tool failed")
                return
            reply = "DSH_OK" if smoke else "已核对"
            if source_test:
                receipt = str(last)
                if "ROLE_PROOF_721" not in receipt or "document:meeting-gate" not in receipt or "revision" not in receipt:
                    self.send_error(400, "Missing permitted body or citation")
                    return
                reply = "ROLE_PROOF_721，依据 document:meeting-gate"
            if list_test or progress_test:
                reply = str(last)
            if title_only_test:
                reply = "FAKE_SOURCE_CONCLUSION"
            if fabricated_test or delegate_test or denied_test or partial_test:
                reply = "FAKE_MODEL_RECEIPT 已经成功创建任务"
            block, delta, reason = {"type":"text","text":""}, {"type":"text_delta","text":reply}, "end_turn"
        else:
            name = next((name for name in ("project_get", "visible_projects", "context_whoami") if name in tools), "activate_notice")
            args = {}
            if "task_create" in tools and "PM_GATE_CREATE" in str(last):
                name, args = "task_create", {"title":"组内创建的验收任务"}
            if source_test:
                if "file_get" not in tools or "task_create" in tools:
                    self.send_error(400, "Incorrect Q&A projection")
                    return
                name, args = "file_get", {"file_ref": "document:meeting-gate"}
            if list_test:
                name = "visible_projects"
            if progress_test:
                name = "delegation_status"
            if title_only_test:
                name, args = "file_search", {"q": "项目纪要"}
            if partial_test:
                name = "task_create"
                args = {"title":"已办事项"} if tool_uses == 0 else {"title":"待澄清事项", "owner_member_id":"ROLE_MISSING_MEMBER"}
            if fabricated_test:
                name = "context_whoami"
            if delegate_test or denied_test:
                if "delegate_project" in tools:
                    name, args = "delegate_project", {"project_id": "locked" if denied_test else "north"}
                elif "task_create" in tools and not denied_test:
                    name, args = "task_create", {"title": "集成角色任务"}
            block = {"type":"tool_use","id":"call_"+uuid.uuid4().hex,"name":name,"input":{}}
            delta, reason = {"type":"input_json_delta","partial_json":json.dumps(args)}, "tool_use"
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
