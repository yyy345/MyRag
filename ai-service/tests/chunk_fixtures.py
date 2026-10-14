def samples():
    return {
        "empty": "",
        "whitespace": " \t\n\ufeff\u3000",
        "short_chinese": "  中文知识🙂\n第二行。  ",
        "exact_boundary": "中" * 2048,
        "long_chinese": "知识库" * 1800,
        "paragraphs": "\n\n".join("段落" + str(i) + "正文" * 500 for i in range(8)),
        "sentences": "知识库包含中文句子。继续学习！请问问题？英文 end.hello!world?test;finish；" * 200,
        "spaces": "long english words and normal spaces " * 300,
        "trim_unicode": "\ufeff" + "中" * 3000 + "\ufeff",
        "emoji_aligned": "🙂" * 3000,
        "emoji_surrogate_boundary": "a" + "🙂" * 3000,
        "emoji_paragraphs": "\n\n".join("中文🙂 " * 300 for _ in range(8)),
    }
