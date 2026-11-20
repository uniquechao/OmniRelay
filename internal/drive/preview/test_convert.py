import base64
import io
import json
from pathlib import Path
import subprocess
import sys
import unittest
from zipfile import ZIP_DEFLATED, ZipFile

from PIL import Image

HERE = Path(__file__).parent
SAMPLES = HERE.parent / "testdata" / "preview"


def render(kind, data, page=1, sheet=0, entry=0):
    result = subprocess.run([sys.executable, "-B", str(HERE / "convert.py"),
                             kind, str(page), str(sheet), str(entry)],
                            input=data, capture_output=True, check=True)
    reply = json.loads(result.stdout)
    body = base64.b64decode(reply.get("body", ""))
    if reply.get("mime") == "application/json":
        body = json.loads(body)
    return reply, body


class FormatSamples(unittest.TestCase):
    def test_office_and_archive(self):
        archive = (SAMPLES / "sample.zip").read_bytes()
        meta, files = render("zip", archive)
        self.assertEqual(meta["meta"]["total"], 7)
        self.assertEqual(files[-1]["state"], "unsupported")
        self.assertEqual(render("zip", archive, entry=2)[1]["text"], "归档正文")
        for kind, entry, page, sheet, expected in [
            ("docx", 3, 1, 0, [{"text": "评审纪要"}, {"table": [["负责人", "张宁"]]}]),
            ("xlsx", 4, 2, 1, [[51, "记录"], [52, "记录"], [53, "记录"]]),
            ("pptx", 5, 2, 0, [{"text": "结论"}, {"table": [["通过"]]}]),
        ]:
            data = (SAMPLES / ("sample." + kind)).read_bytes()
            meta, body = render(kind, data, page, sheet)
            self.assertEqual(body, expected)
            if kind == "xlsx":
                self.assertEqual(meta["meta"]["sheets"], ["概览", "明细"])
            self.assertEqual(render("zip", archive, page, sheet, entry)[1], expected)

    def test_scanned_pages(self):
        pdf = (SAMPLES / "scan.pdf").read_bytes()
        for page, channel in [(1, 0), (2, 2)]:
            reply, body = render("pdf", pdf, page)
            self.assertEqual(reply["meta"]["pages"], 2)
            pixel = Image.open(io.BytesIO(body)).convert("RGB").getpixel((20, 20))
            self.assertGreater(pixel[channel], 245)
        self.assertEqual(render("pdf", pdf, 3)[0]["stage"], "invalid_page")
        self.assertEqual(render("zip", (SAMPLES / "sample.zip").read_bytes(), 2, entry=6)[1], body)

    def test_states_and_coverage(self):
        rows = [{"fields": {"名称": "首行"}}] * 50 + [{"fields": {"备注": "末行"}}]
        reply, data = render("base", json.dumps([dict(name="任务", records=rows)]).encode(), 2)
        self.assertEqual(data, rows[-1:])
        self.assertEqual(reply["meta"]["fields"], ["名称", "备注"])
        self.assertEqual(reply["meta"]["first"], 51)
        for kind in ["doc", "xls", "ppt"]:
            self.assertEqual(render(kind, b"")[0]["stage"], "unsupported")
        self.assertEqual(render("pdf", b"invalid")[0]["stage"], "failed")
        archive = io.BytesIO()
        with ZipFile(archive, "w", ZIP_DEFLATED) as z:
            z.writestr("大.txt", bytes(8 * 1024**2 + 1))
        self.assertEqual(render("zip", archive.getvalue(), entry=1)[0]["stage"], "over_limit")
        encrypted = (SAMPLES / "encrypted.zip").read_bytes()
        self.assertEqual(render("zip", encrypted)[1][0]["state"], "encrypted")
        self.assertEqual(render("zip", encrypted, entry=1)[0], {"stage": "encrypted"})
        for kind in ("pdf", "docx"):
            encrypted = (SAMPLES / ("encrypted." + kind)).read_bytes()
            self.assertEqual(render(kind, encrypted)[0], {"stage": "encrypted"})


if __name__ == "__main__":
    unittest.main()
