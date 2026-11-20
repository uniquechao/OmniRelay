"""读取一个已授权版本，返回一个页面。输入和输出均经管道传递。"""
import base64
import io
import json
from pathlib import PurePosixPath
import resource
import sys
from zipfile import ZipFile

MIB = 1 << 20
ROWS = 50
TEXT = 4096
MEMBER_LIMIT = 8 * MIB
TOTAL_LIMIT = 32 * MIB
OUTPUT_LIMIT = 3 * MIB


class State(Exception):
    pass


def window(total, page, size):
    pages = max(1, (total + size - 1) // size)
    if not 1 <= page <= pages:
        raise State("invalid_page")
    start = (page - 1) * size
    end = min(total, start + size)
    return slice(start, end), dict(page=page, pages=pages, total=total,
                                   first=start + 1 if total else 0, last=end)


def inspect_archive(archive, budget):
    items = archive.infolist()
    if len(items) > 1024:
        raise State("over_limit")
    budget[0] -= sum(item.file_size for item in items)
    if budget[0] < 0:
        raise State("over_limit")
    return items


def member_state(item):
    if item.flag_bits & 1:
        return "encrypted"
    if item.file_size > MEMBER_LIMIT:
        return "over_limit"
    if item.is_dir() or suffix(item.filename) not in ("txt", "pdf", "docx", "xlsx", "pptx"):
        return "unsupported"
    return "ready"


def suffix(name):
    return PurePosixPath(name).suffix.lower().lstrip(".")


def grid(table):
    return [[cell.text for cell in row.cells] for row in table.rows]


def slide_blocks(shapes):
    from pptx.enum.shapes import MSO_SHAPE_TYPE
    for shape in shapes:
        if shape.shape_type == MSO_SHAPE_TYPE.GROUP:
            yield from slide_blocks(shape.shapes)
        elif shape.has_table:
            yield {"table": grid(shape.table)}
        elif shape.has_text_frame:
            yield {"text": shape.text}


def spreadsheet(stream, page, sheet):
    from openpyxl import load_workbook
    book = load_workbook(stream, read_only=True, data_only=False, keep_links=False)
    sheets = [ws for ws in book.worksheets if ws.sheet_state == "visible"]
    names = [ws.title for ws in sheets]
    if not 0 <= sheet < len(names):
        raise State("invalid_sheet")
    ws = sheets[sheet]
    # 使用实际行列，避免生成器写入的错误维度截断读取结果。
    ws.reset_dimensions()
    height = width = 0
    rows = []
    start = (page - 1) * ROWS
    for height, values in enumerate(ws.iter_rows(values_only=True), 1):
        width = max(width, len(values))
        if height > 100000 or width > 512:
            raise State("over_limit")
        if start < height <= start + ROWS:
            rows.append(values)
    _, meta = window(height, page, ROWS)
    return meta | dict(sheets=names, sheet=sheet, columns=width), rows


def convert(kind, data, page, sheet, entry, budget):
    stream = io.BytesIO(data)
    if entry and kind != "zip":
        raise State("invalid_entry")
    if sheet and kind not in ("zip", "base", "xlsx"):
        raise State("invalid_sheet")
    if kind in ("docs", "minutes", "txt"):
        content = data.decode("utf-8-sig")
        part, meta = window(len(content), page, TEXT)
        return meta, {"text": content[part]}
    if kind == "base":
        tables = json.loads(data)
        if not tables:
            _, meta = window(0, page, ROWS)
            if sheet:
                raise State("invalid_sheet")
            return meta | dict(sheets=[], fields=[]), []
        if not 0 <= sheet < len(tables):
            raise State("invalid_sheet")
        records = tables[sheet]["records"]
        fields = list(dict.fromkeys(k for row in records for k in row["fields"]))
        part, meta = window(len(records), page, ROWS)
        return meta | dict(sheets=[t["name"] for t in tables], sheet=sheet,
                           fields=fields), records[part]
    if kind == "pdf":
        import pypdfium2 as pdf
        try:
            doc = pdf.PdfDocument(data)
        except pdf.PdfiumError as exc:
            if "password" in str(exc).lower():
                raise State("encrypted") from exc
            raise
        _, meta = window(len(doc), page, 1)
        try:
            pdf_page = doc[page - 1]
            width, height = pdf_page.get_size()
            bitmap = pdf_page.render(scale=min(2, 1440 / max(width, height)))
            output = io.BytesIO()
            bitmap.to_pil().save(output, format="PNG")
            return meta, output.getvalue()
        finally:
            doc.close()
    if kind not in ("zip", "docx", "xlsx", "pptx"):
        raise State("unsupported")
    with ZipFile(stream) as archive:
        items = inspect_archive(archive, budget)
        if kind == "zip":
            if entry:
                if entry > len(items):
                    raise State("invalid_entry")
                item = items[entry - 1]
                state = member_state(item)
                if state != "ready":
                    raise State(state)
                meta, result = convert(suffix(item.filename), archive.read(item),
                                       page, sheet, 0, budget)
                return meta | dict(entry=entry, name=item.filename), result
            if sheet:
                raise State("invalid_sheet")
            part, meta = window(len(items), page, ROWS)
            return meta, [dict(entry=i, name=f.filename, size=f.file_size,
                               directory=f.is_dir(), state=member_state(f))
                          for i, f in enumerate(items[part], part.start + 1)]
        if any(f.flag_bits & 1 for f in items):
            raise State("encrypted")
        if any(f.file_size > MEMBER_LIMIT for f in items):
            raise State("over_limit")
    stream.seek(0)
    if kind == "docx":
        from docx import Document
        from docx.table import Table
        blocks = list(Document(stream).iter_inner_content())
        part, meta = window(len(blocks), page, ROWS)
        return meta, [{"table": grid(b)} if isinstance(b, Table)
                      else {"text": b.text} for b in blocks[part]]
    if kind == "xlsx":
        return spreadsheet(stream, page, sheet)
    from pptx import Presentation
    slides = Presentation(stream).slides
    _, meta = window(len(slides), page, 1)
    return meta, list(slide_blocks(slides[page - 1].shapes))


def main():
    resource.setrlimit(resource.RLIMIT_AS, (256 * MIB, 256 * MIB))
    resource.setrlimit(resource.RLIMIT_CPU, (8, 9))
    kind, page, sheet, entry = sys.argv[1:]
    try:
        data = sys.stdin.buffer.read(16 * MIB + 1)
        if len(data) > 16 * MIB:
            raise State("over_limit")
        meta, result = convert(kind, data, int(page), int(sheet), int(entry), [TOTAL_LIMIT])
        mime = "image/png" if isinstance(result, bytes) else "application/json"
        if mime == "application/json":
            result = json.dumps(result, ensure_ascii=False, default=str).encode()
        if len(result) + len(json.dumps(meta).encode()) > OUTPUT_LIMIT:
            raise State("over_limit")
        reply = dict(stage="ready", meta=meta, mime=mime,
                     body=base64.b64encode(result).decode("ascii"))
    except State as exc:
        reply = dict(stage=str(exc))
    except MemoryError:
        reply = dict(stage="over_limit")
    except Exception:
        reply = dict(stage="failed")
    print(json.dumps(reply))


if __name__ == "__main__":
    main()
