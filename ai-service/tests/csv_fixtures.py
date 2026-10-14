"""Local CSV samples and explicit expectations for intentional legacy fixes."""
def samples():
    return {
        "basic.csv": "姓名,年龄\n张三,18\n李四,20".encode(),
        "gbk.CSV": "名称,说明\n中文,编码".encode("gbk"),
        "bom.csv": "\ufeff名称,备注\r\n测试,完成\r\n".encode(),
        "comma.csv": b'name,note\nA,"one,two"\n',
        "short.csv": b'a,b,c\n1,2\n3,,5\n',
        "blank_rows.csv": b'a,b\n\n,\n  , \n1,2\n',
        "nul.csv": b'a,b\n1,x\x00y',
        "empty.csv": b' \n,\n',
        "quotes.csv": b'name,note\nA,"say ""hello"""',
        "multiline.csv": '名称,备注\r\n中文,"第一行\r\n第二行"\r\n'.encode(),
        "markdown.csv": b'name,note\nA,"a|b\\c"',
        "extra.csv": b'a,b\n1,2,3',
        "unclosed.csv": b'a,b\n1,"unclosed',
    }


FIXES = {
    "quotes.csv": "name | note\n--- | ---\nA | say \"hello\"",
    "multiline.csv": "名称 | 备注\n--- | ---\n中文 | 第一行 ↵ 第二行",
    "markdown.csv": "name | note\n--- | ---\nA | a\\|b\\\\c",
    "extra.csv": None,
    "unclosed.csv": None,
}
