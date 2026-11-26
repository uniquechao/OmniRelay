"""DSH instance lifecycle for a private, single-writer Linux runtime volume."""

import fcntl
import hashlib
import json
import os
import re
import sys
import tempfile
from contextlib import contextmanager
from pathlib import Path

from deepseek_harness import DeepSeekHarness


class ConfigurationConflict(ValueError):
    """The owner has already applied a different revision to this Agent."""


def legacy_personas(root):
    """Inventory old local edits without exposing the instructions in logs."""
    if not root.exists():
        return
    for directory in sorted(root.iterdir()):
        if not re.fullmatch(r"[0-9a-f]{64}", directory.name) or not directory.is_dir():
            continue
        origin, persona = directory / "origin.json", directory / "persona.md"
        if not origin.exists() and not persona.exists():
            continue
        # A missing file must be visible to the operator. Never guess a baseline.
        try:
            record = json.loads(origin.read_text(encoding="utf-8"))
            baseline = record.get("template_sha256") if isinstance(record, dict) else None
            if (not isinstance(baseline, str) or not re.fullmatch(r"[a-f0-9]{64}", baseline)
                    or record.get("agent_id") != directory.name):
                raise ValueError("invalid original template identity")
            digest = hashlib.sha256(persona.read_bytes()).hexdigest()
        except (OSError, ValueError):
            yield {"agent": directory.name, "state": "incomplete"}
            continue
        if digest != baseline:
            yield {"agent": directory.name, "state": "locally_modified", "original_sha256": baseline,
                   "local_sha256": digest, "file": str(persona)}


@contextmanager
def open_instance(root: Path, agent_id: str, template: Path, *, role, config_version, persona,
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
            with tempfile.TemporaryDirectory(dir=root, prefix=".init-") as temp:
                staged = Path(temp) / "instance"
                staged.mkdir(mode=0o700)
                (staged / "workspace").mkdir()
                (staged / "home").mkdir()
                staged.rename(directory)
        prompt = template.read_text(encoding="utf-8")
        if persona:
            prompt += "\n\n管理员配置的人设与工作说明\n" + persona
        # This checkpoint is an acknowledgement. Desired configuration is owned by Go.
        checkpoint = directory / "applied-config.json"
        applied = {"role": role, "version": config_version,
                   "persona_sha256": hashlib.sha256(persona.encode()).hexdigest()}
        if checkpoint.exists():
            previous = json.loads(checkpoint.read_text(encoding="utf-8"))
            if (previous["role"] != role or previous["version"] > config_version
                    or previous["version"] == config_version and previous != applied):
                raise ConfigurationConflict("configuration no longer matches this Agent")
        with DeepSeekHarness(
            profile="sdk-minimal",
            cwd=str(directory / "workspace"), dsh_home=str(directory / "home"),
            patches=patches, env={**(tool_env or {}), "DSH_SYSTEM_PROMPT": prompt},
            base_url=base_url, api_key=api_key, model=model,
            initialize_timeout_seconds=15, request_timeout_seconds=60,
        ) as harness:
            with tempfile.NamedTemporaryFile("w", dir=directory, encoding="utf-8", delete=False) as f:
                json.dump(applied, f)
            Path(f.name).replace(checkpoint)
            yield harness


if __name__ == "__main__":
    if sys.argv[1:] != ["legacy-personas"]:
        raise SystemExit("usage: python instance.py legacy-personas")
    root = Path(os.environ.get("AGENT_VOLUME", "/var/lib/omnirelay-agent")) / "agents"
    print(json.dumps(list(legacy_personas(root)), ensure_ascii=False))
