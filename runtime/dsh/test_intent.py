"""The model may report a structured interpretation, but cannot execute tools here."""
import io
import json
import unittest
import urllib.request
from unittest.mock import patch
from intent import PlanProtocolError, semantic_plan

CATALOG = [{"name":"briefing_save", "literal":["business_date"], "content":["title","body"],
            "required":["business_date","title","body"]}]
CFG = {"base_url":"https://model.invalid/anthropic","model":"fixture","api_key":"fixture"}
QUESTIONS = {
    "给我打开已经保存的简报。": ("inspect_existing_or_answer", "read"),
    "看一下简报要怎么保存？": ("inspect_existing_or_answer", "read"),
    "读这份纪要，给我起草明天过目的简报。": ("compose_for_review","draft"),
    "归纳上周的群聊成果并写进简报。": ("apply_business_change_now","execute"),
    "确认写入刚才的第二项。": ("approve_shown_proposal","confirm"),
}

class PlannerTest(unittest.TestCase):
    def run_model(self, text, respond):
        observed = []
        class Model:
            def open(inner, req, timeout):
                payload=json.loads(req.data)
                observed.append(payload)
                self.assertLessEqual(timeout,72)
                self.assertGreater(timeout,0)
                return io.BytesIO(json.dumps(respond(payload)).encode())
        with patch("urllib.request.build_opener",return_value=Model()):
            result=semantic_plan(CFG,{"request":{"text":text,"intent":"project","private":False},"catalog":CATALOG,"server_fields":["expected_version","generation"]},urllib.request.HTTPRedirectHandler())
        return result,observed

    def respond(self, payload):
        tool=payload["tool_choice"]["name"]
        self.assertEqual(payload["temperature"],0)
        self.assertEqual(payload["thinking"],{"type":"disabled"})
        self.assertEqual([t["name"] for t in payload["tools"]],[tool])
        self.assertEqual(len(payload["messages"]),1)
        content=payload["messages"][0]["content"]
        if tool=="classify_request":
            kind,mode=QUESTIONS[content]
            value={"dialogue_act":"approve_prior_action" if mode=="confirm" else "other","kind":kind,"intent":"project","evidence_period":None}
            if mode=="confirm":
                value["selection"]=2
            if mode=="execute":
                value["evidence_period"]={"kind":"last_week","evidence":"上周"}
        else:
            text=json.loads(content)["text"]
            _,mode=QUESTIONS[text]
            self.assertEqual(payload["tools"][0]["input_schema"]["properties"]["mode"]["enum"],[mode])
            value={"mode":mode,"actions":[{"tool":"briefing_save","evidence":text,
                   "fields":{"business_date":"@today"},"generate":["title","body"]}]}
        return {"stop_reason":"tool_use","content":[{"type":"tool_use","name":tool,"input":value}]}

    def test_repeated_structured_results(self):
        for text,(_,mode) in QUESTIONS.items():
            for _ in range(3):
                with self.subTest(text=text):
                    plan,calls=self.run_model(text,self.respond)
                    self.assertEqual(plan["mode"],mode)
                    self.assertEqual(len(calls),2 if mode in ("draft","execute") else 1)
                    self.assertEqual(bool(plan["actions"]),mode in ("draft","execute"))
                    if mode=="execute":
                        self.assertEqual(plan["evidence_period"]["kind"],"last_week")

    def test_approval_is_resolved_by_gateway_even_if_model_cannot_locate_target(self):
        def proposal_absent(p):
            return {"stop_reason":"tool_use","content":[{"type":"tool_use","name":"classify_request",
                    "input":{"dialogue_act":"approve_prior_action","kind":"needs_clarification",
                             "intent":"project","clarification":"哪份草案？","evidence_period":None}}]}
        plan, calls=self.run_model("确认写入",proposal_absent)
        self.assertEqual((plan["mode"],plan["actions"],len(calls)),("confirm",[],1))
        self.assertNotIn("clarification",plan)

    def test_free_text_or_wrong_schema_cannot_become_an_empty_success(self):
        for malformed in [
            {"stop_reason":"end_turn","content":[{"type":"text","text":"{}"}]},
            {"stop_reason":"tool_use","content":[{"type":"tool_use","name":"classify_request","input":{}}]},
            {"stop_reason":"tool_use","content":[{"type":"tool_use","name":"briefing_save","input":{}}]},
        ]:
            calls=[]
            def fail(payload):
                calls.append(payload)
                return malformed
            with self.assertRaises(PlanProtocolError):
                self.run_model(next(iter(QUESTIONS)),fail)
            self.assertEqual(len(calls),2)

    def test_domain_version_and_content_placeholders_do_not_reach_authorization(self):
        def model(p):
            result=self.respond(p)
            if p["tool_choice"]["name"]=="report_plan":
                self.assertNotIn("expected_version",p["system"])
                action=result["content"][0]["input"]["actions"][0]
                action["generate"]+=["expected_version","title"]
                action["fields"].update(expected_version="999",title="@generate",body="@generate")
            return result
        p,_=self.run_model("读这份纪要，给我起草明天过目的简报。",model)
        self.assertEqual(p["actions"][0]["fields"],{"business_date":"@today"})
        self.assertEqual(p["actions"][0]["generate"],["title","body"])

    def test_one_malformed_reply_then_recovery(self):
        calls=0
        def recover(p):
            nonlocal calls
            calls+=1
            if calls==1:return {"stop_reason":"end_turn","content":[]}
            return self.respond(p)
        p,_=self.run_model("给我打开已经保存的简报。",recover)
        self.assertEqual((p["mode"],calls),("read",2))

if __name__=="__main__":
    unittest.main()
