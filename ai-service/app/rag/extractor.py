import re

from app.rag.chunker import utf16_length
from app.rag.generator import utf16_prefix
from app.providers.chat import ChatError

EXTRACT_PROMPT = '''你是一个知识提炼专家。请分析以下对话记录，从中提取出有价值的知识点。要求：
1. 标题用 "## " 开头，正文用 Markdown 格式，正文不少于 200 字
2. 正文包含核心要点和关键信息
3. 各知识点之间用 "---" 分隔
4. 忽略纯闲聊和问候
5. 如果没有可提炼的知识，返回 "NONE"'''


def parse_sections(text):
    if not isinstance(text,str) or "\x00" in text or len(text)>200_000:
        raise ChatError("invalid_extraction_response")
    if text.strip()=="NONE":
        return []
    values=[]
    for section in text.split("---"):
        section=section.strip()
        match=re.match(r"^##[ \t]+([^\r\n]+)(?:\r?\n|$)",section)
        if not match:
            continue
        title=utf16_prefix(match.group(1).strip(),200)
        content=section[match.end():].strip()
        if not title or utf16_length(content)<200:
            continue
        values.append({"title":title,"content":content})
        if len(values)>20:
            raise ChatError("extraction_response_limit")
    return values


class Extractor:
    def __init__(self,chat):
        self.chat=chat

    async def extract(self,payload):
        if payload.mode=="scheduled" and sum(message.role=="user" for message in payload.history)<2:
            return []
        preview="\n\n".join(f"[{'用户' if message.role=='user' else 'AI'}] {utf16_prefix(message.content,800)}" for message in reversed(payload.history))
        text=await self.chat.complete([{"role":"system","content":EXTRACT_PROMPT},{"role":"user","content":"对话记录：\n"+utf16_prefix(preview,16000)}],temperature=0.5 if payload.mode=="scheduled" else 0.3)
        return parse_sections(text)
