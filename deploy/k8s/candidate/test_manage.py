"""检查租约互斥、凭据绑定、次数和失败后的状态。"""
import contextlib
import os
import secrets
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import cluster
import manage


class RunTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(dir=cluster.PRIVATE.parent)
        self.addCleanup(temporary.cleanup)
        self.stack = contextlib.ExitStack()
        self.addCleanup(self.stack.close)
        for module in (cluster, manage):
            self.stack.enter_context(patch.object(module, "PRIVATE", Path(temporary.name)))
        self.stack.enter_context(patch.object(manage, "verify"))
        self.adapter = self.stack.enter_context(patch.object(manage, "exchange", side_effect=self.reply))
        self.lock = {"pod":secrets.token_hex(), "active":True}
        manage.save("candidate.json", self.lock)
        aliases = {key:secrets.token_hex(4) for key in ("identity","session","project","chat")}
        op = secrets.token_hex()
        self.request = {"issue":secrets.token_hex(), "count":1,"ttl":60,"resources":aliases,"operations":[op]}
        self.registry = {"resources":{k:{v:secrets.token_hex()} for k,v in aliases.items()},
                         "operations":{op:"消息"},"capabilities":{"消息":{"socket":secrets.token_hex()}}}
        self.op = op

    def reply(self, _, body):
        if body["method"]=="readiness":
            return {"ready":True}
        self.assertEqual(body["resources"],manage.read("run.json")["resources"])
        return {"attempt":body["attempt"],"finished":True}

    def open(self):
        result=manage.new_run(self.request,self.lock,self.registry)
        return {**result,"version":1,"operation":self.op}

    def test_fencing(self):
        first=self.open()
        with self.assertRaises(ValueError):
            self.open()
        with self.assertRaises(ValueError):
            manage.dispatch({**first,"credential":secrets.token_hex()})
        manage.dispatch(first)
        with self.assertRaises(ValueError):
            manage.dispatch(first)
        manage.revoke()
        second=self.open()
        with self.assertRaises(ValueError):
            manage.dispatch(first)
        with patch("manage.time.time",return_value=second["expires"]+1):
            with self.assertRaises(ValueError):
                manage.dispatch(second)
            self.open()

    def test_web_run_scopes_and_revocation(self):
        web={"origin":"https://candidate.example.test","appID":"app","tenantKey":"tenant"}
        manage.save("target.json",{"web":web})
        (manage.PRIVATE/"web").mkdir()
        self.registry["resources"]["callback"]={"验收":web["origin"]+"/auth/feishu/callback"}
        self.request["resources"]["callback"]="验收"
        self.registry["capabilities"]["OAuth"]={"socket":"test"}
        self.registry["operations"]["oauth.identity"]="OAuth"
        self.request["operations"]=["oauth.identity",self.op]
        with self.assertRaises(ValueError):
            self.open()
        self.request["operations"]=["oauth.identity"]
        with patch("manage.command"):
            opened=self.open()
        published=manage.read("web/web.json")
        self.assertEqual(published["pod"],self.lock["pod"])
        self.assertEqual(published["credential"],opened["credential"])
        self.assertEqual(manage.read("run.json")["resources"]["appID"],"app")
        manage.revoke()
        self.assertIsNone(manage.read("web/web.json"))

    def test_uncertain_call(self):
        req=self.open()
        self.adapter.side_effect=OSError()
        with self.assertRaises(OSError):
            manage.dispatch(req)
        self.assertEqual(manage.read("run.json")["remaining"],0)
        with self.assertRaises(ValueError):
            self.open()

    def test_message_binding_and_independent_stops(self):
        self.lock.update(sha="a"*40,image="candidate-image")
        manage.save("candidate.json",self.lock)
        manage.save("target.json",{"id":"a"*24,"context":"fixture",
            "web":{"origin":"https://candidate.example.test","appID":"app","tenantKey":"tenant"}})
        manage.save("receiver.json",{"sha":"b"*40,"rollback":"previous-digest"})
        (manage.PRIVATE/"web").mkdir()
        self.request["operations"]=["message.receive","message.reply"]
        self.registry["operations"].update({op:"消息" for op in self.request["operations"]})
        self.request["resources"]["privateChat"]="私聊"
        self.registry["resources"]["privateChat"]={"私聊":"oc_private"}
        with (
            patch("manage.command"),
            patch("manage.receiver_call",return_value={"ReceiverSHA":"b"*40,"ReceiverRollback":"previous-digest"}) as receiver,
            patch("manage.Cluster.candidate_pod",return_value={"metadata":{"uid":self.lock["pod"]}}),
        ):
            opened=manage.new_run(self.request,self.lock,self.registry)
            binding=receiver.call_args.kwargs["Binding"]
            self.assertEqual(binding["PrivateChat"],"oc_private")
            self.assertEqual(binding["GroupChat"],manage.read("run.json")["resources"]["chat"])
            self.assertTrue(binding["MayReply"])
            self.assertFalse(binding["MayReconcile"])
            req={**opened,"version":1}
            with self.assertRaises(ValueError):
                manage.dispatch({**req,"credential":"wrong","operation":"message.claim"})
            with self.assertRaises(ValueError):
                manage.dispatch({**req,"operation":"message.reply","payload":{
                    "source":"om_message","text":"test","target":"oc_unrelated"}})
            manage.dispatch({**req,"operation":"message.claim"})
            manage.stop_messages(manage.read("run.json"),True,False)
            with self.assertRaises(ValueError):
                manage.dispatch({**req,"operation":"message.claim"})
            manage.dispatch({**req,"operation":"message.reply","payload":{"source":"om_message","text":"test"}})
            self.assertEqual(manage.read("run.json")["remaining"],0)
            with self.assertRaises(ValueError):
                manage.dispatch({**req,"operation":"message.reply","payload":{"source":"om_message","text":"test"}})
            manage.stop_messages(manage.read("run.json"),False,True)
            self.assertTrue(manage.read("run.json")["outboundRevoked"])


if __name__=="__main__":
    cluster.PRIVATE.parent.mkdir(parents=True,exist_ok=True,mode=0o700)
    unittest.main()
