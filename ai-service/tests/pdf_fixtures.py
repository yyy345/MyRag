"""Deterministic local PDFs. No user documents, provider calls or font downloads."""
from io import BytesIO

from PIL import Image
from pypdf import PdfReader, PdfWriter
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.cidfonts import UnicodeCIDFont
from reportlab.pdfgen.canvas import Canvas
from reportlab.lib.utils import ImageReader


def text_pdf(pages):
    buffer = BytesIO()
    canvas = Canvas(buffer, invariant=1, pageCompression=0)
    pdfmetrics.registerFont(UnicodeCIDFont("STSong-Light"))
    for lines in pages:
        canvas.setFont("STSong-Light" if any(any(ord(c) > 127 for c in line) for line in lines) else "Helvetica", 12)
        for index, text in enumerate(lines):
            canvas.drawString(40, 780 - 24 * index, text)
        canvas.showPage()
    canvas.save()
    return buffer.getvalue()


def samples():
    chinese = text_pdf([["知识库解析测试", "第一行中文 English 123"], ["第二页内容", "结束"]])
    writer = PdfWriter()
    writer.append(PdfReader(BytesIO(chinese)))
    writer.encrypt("fixture-password-only")
    encrypted = BytesIO()
    writer.write(encrypted)
    scanned = BytesIO()
    canvas = Canvas(scanned, invariant=1)
    canvas.drawImage(ImageReader(Image.new("RGB", (8, 8), "black")), 40, 600, 200, 200)
    canvas.showPage()
    canvas.save()
    blank = BytesIO()
    writer = PdfWriter()
    writer.add_blank_page(width=612, height=792)
    writer.write(blank)
    empty = BytesIO()
    PdfWriter().write(empty)
    return {
        "chinese.PDF": chinese,
        "english.pdf": text_pdf([["Page one", "Line two"], ["Page two"]]),
        "scanned.pdf": scanned.getvalue(),
        "blank.pdf": blank.getvalue(),
        "encrypted.pdf": encrypted.getvalue(),
        "corrupt.pdf": b"%PDF-1.7\n1 0 obj << broken",
        "fake.pdf": b"this is not a PDF",
        "empty.pdf": empty.getvalue(),
    }


if __name__ == "__main__":
    import base64
    import json
    print(json.dumps([[name, base64.b64encode(data).decode()] for name, data in samples().items()]))
