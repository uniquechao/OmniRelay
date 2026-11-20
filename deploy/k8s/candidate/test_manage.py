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

    def test_uncertain_call(self):
        req=self.open()
        self.adapter.side_effect=OSError()
        with self.assertRaises(OSError):
            manage.dispatch(req)
        self.assertEqual(manage.read("run.json")["remaining"],0)
        with self.assertRaises(ValueError):
            self.open()


if __name__=="__main__":
    cluster.PRIVATE.parent.mkdir(parents=True,exist_ok=True,mode=0o700)
    unittest.main()
