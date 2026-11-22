"""DSH instance lifecycle for a private, single-writer Linux runtime volume."""

import fcntl
import hashlib
import json
import re
import tempfile
from contextlib import contextmanager
from pathlib import Path

from deepseek_harness import DeepSeekHarness


@contextmanager
def open_instance(root: Path, agent_id: str, template: Path, *,
                  base_url: str, api_key: str, model: str, patches, tool_env):
    """Load a registered Agent on its owner. The caller supplies trusted paths."""
    if not re.fullmatch(r"[0-9a-f]{64}", agent_id):
        raise ValueError("invalid agent ID")
    root = root.resolve()
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    directory = root / agent_id
    with (root / (agent_id + ".lock")).open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if not directory.exists():
            persona = template.read_bytes()
            with tempfile.TemporaryDirectory(dir=root, prefix=".init-") as temp:
                staged = Path(temp) / "instance"
                staged.mkdir(mode=0o700)
                (staged / "workspace").mkdir()
                (staged / "home").mkdir()
                (staged / "persona.md").write_bytes(persona)
                (staged / "origin.json").write_text(json.dumps({
                    "agent_id": agent_id, "template_sha256": hashlib.sha256(persona).hexdigest()
                }), encoding="utf-8")
                staged.rename(directory)
        with DeepSeekHarness(
            profile="sdk-minimal",
            cwd=str(directory / "workspace"), dsh_home=str(directory / "home"),
            patches=patches, env={**(tool_env or {}), "DSH_SYSTEM_PROMPT": (directory / "persona.md").read_text(encoding="utf-8")},
            base_url=base_url, api_key=api_key, model=model,
            initialize_timeout_seconds=15, request_timeout_seconds=60,
        ) as harness:
            yield harness
