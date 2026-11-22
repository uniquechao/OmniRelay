"""Verify durable tool slots against the Runtime image's own SQLite code."""
import json
import ssl
import tempfile
from pathlib import Path

from runtime import Runtime

with tempfile.TemporaryDirectory() as root:
    options = (Path(root), "runtime", "https://gateway", ssl.create_default_context())
    first = Runtime(*options)
    assert first.command_slot("work", "task_create", {"title": "first"}) == 1
    first.volume_lock.close()
    recovered = Runtime(*options)
    with recovered.db() as db:
        slot, pending = db.execute("SELECT slot,pending FROM tool_commands WHERE work_id='work'").fetchone()
    assert slot == 1 and json.loads(pending)["args"] == {"title": "first"}
    assert recovered.command_slot("work", "task_create", {"title": "first"}) == slot
    recovered.settled("work", slot)
    assert recovered.command_slot("work", "task_add_progress", {"body": "next"}) == 2
    assert recovered.command_slot("another-work", "task_create", {"title": "first"}) == 1
    with recovered.db() as db:
        assert db.execute("SELECT pending FROM tool_commands WHERE work_id='work' AND slot=1").fetchone() == (None,)
    recovered.volume_lock.close()
print("Persistent project tool command journal passed")
