"""Small generated OOXML archives; no Word installation or downloaded documents."""
from io import BytesIO
from zipfile import ZIP_DEFLATED, ZipFile

W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
R = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"


def archive(parts):
    output = BytesIO()
    with ZipFile(output, "w", ZIP_DEFLATED) as zipped:
        for name, body in parts.items():
            zipped.writestr(name, body)
    return output.getvalue()


def document(body):
    return archive({
        "[Content_Types].xml": '<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>',
        "_rels/.rels": f'<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="{R}/officeDocument" Target="word/document.xml"/></Relationships>',
        "word/document.xml": f'<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="{W}" xmlns:r="{R}"><w:body>{body}<w:sectPr/></w:body></w:document>',
        "word/_rels/document.xml.rels": '<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"/>',
    })


def paragraph(text):
    from xml.sax.saxutils import escape
    return f'<w:p><w:r><w:t xml:space="preserve">{escape(text)}</w:t></w:r></w:p>'


def samples():
    table = '<w:tbl><w:tr>' + ''.join('<w:tc>' + paragraph(cell) + '</w:tc>' for cell in ['列一', '列二']) + '</w:tr><w:tr><w:tc>' + paragraph('单元格第一段') + paragraph('第二段') + '</w:tc><w:tc>' + paragraph('数据🙂') + '</w:tc></w:tr></w:tbl>'
    return {
        "paragraphs.DOCX": document(paragraph('标题 中文🙂') + paragraph('前后  空格') + '<w:p/>' + paragraph('结束')),
        "table.docx": document(paragraph('表前') + table + paragraph('表后')),
        "runs.docx": document('<w:p><w:r><w:t>第一段</w:t></w:r><w:r><w:tab/><w:t>第二段</w:t><w:br/><w:t>换行后</w:t></w:r></w:p>'),
        "nested.docx": document('<w:tbl><w:tr><w:tc>' + paragraph('外层') + '<w:tbl><w:tr><w:tc>' + paragraph('内层') + '</w:tc></w:tr></w:tbl>' + paragraph('外层尾') + '</w:tc></w:tr></w:tbl>'),
        "blank.docx": document('<w:p/>'),
        "corrupt.docx": b'PK\x03\x04broken',
        "fake.docx": b'not a zip archive',
        "missing.docx": archive({'readme.txt': 'missing document'}),
        "malformed.docx": archive({'word/document.xml': '<broken'}),
    }
