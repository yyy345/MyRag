"""CSV records to Markdown tables, including quoted multiline fields."""
import csv
from io import StringIO

from app.ingestion.parser import JS_WHITESPACE, ParseError, decode_chinese_text

MAX_CSV_ROWS = 100_000
MAX_CSV_COLUMNS = 1000
MAX_CSV_CHARACTERS = 2_000_000
# Set once at module initialization, never mutate the process-wide limit per request.
csv.field_size_limit(MAX_CSV_CHARACTERS)


def markdown_cell(value: str) -> str:
    value = value.replace("\r\n", "\n").replace("\r", "\n")
    # HTML is disabled in the Vue renderer; use a visible line-break marker
    # rather than injecting <br> or splitting a Markdown table row.
    return value.replace("\\", "\\\\").replace("|", "\\|").replace("\n", " ↵ ")


def parse_csv(content: bytes) -> str:
    text = decode_chinese_text(content).replace("\x00", "").lstrip("\ufeff")
    lines = []
    width = None
    characters = 0
    try:
        rows = csv.reader(StringIO(text, newline=""), strict=True, skipinitialspace=True)
        for row_number, row in enumerate(rows, 1):
            if row_number > MAX_CSV_ROWS or len(row) > MAX_CSV_COLUMNS:
                raise ParseError("csv_size_limit", "CSV 行数或列数超过限制", 413)
            cells = [value.strip(JS_WHITESPACE) for value in row]
            if not any(cells):
                continue
            if width is None:
                width = len(cells)
            elif len(cells) > width:
                raise ParseError("invalid_csv_columns", "CSV 数据列数超过表头列数")
            cells += [""] * (width - len(cells))
            line = " | ".join(markdown_cell(value) for value in cells)
            characters += len(line) + 1
            if not lines:
                alignment = " | ".join(["---"] * width)
                characters += len(alignment) + 1
            if characters > MAX_CSV_CHARACTERS:
                raise ParseError("csv_size_limit", "CSV 解析文本过长", 413)
            if not lines:
                lines.extend([line, alignment])
            else:
                lines.append(line)
    except csv.Error as error:
        if "field larger than field limit" in str(error):
            raise ParseError("csv_size_limit", "CSV 单元格文本过长", 413) from None
        raise ParseError("invalid_csv", "CSV 引号或记录格式不正确") from None
    if not lines:
        raise ParseError("empty_document", "CSV 文件无有效内容")
    return "\n".join(lines)
