import unittest
from app.rag.chunker import CHUNK_SIZE, CHUNK_OVERLAP, chunk_text, utf16_length
from chunk_fixtures import samples


class ChunkerTests(unittest.TestCase):
    def test_empty_short_and_boundaries(self):
        self.assertEqual(chunk_text(""), [])
        self.assertEqual(chunk_text("\ufeff \t\u3000"), [])
        self.assertEqual(chunk_text("  中文🙂  "), [{"index": 0, "content": "中文🙂"}])
        self.assertEqual(chunk_text("中" * 2048), [{"index": 0, "content": "中" * 2048}])
        parts = chunk_text("中" * 4000)
        self.assertEqual([len(part["content"]) for part in parts], [2048, 2048, 304])
        self.assertEqual(CHUNK_OVERLAP, 200)

    def test_unicode_limits_and_contiguous_indices(self):
        for name, text in samples().items():
            chunks = chunk_text(text)
            self.assertEqual([chunk["index"] for chunk in chunks], list(range(len(chunks))), name)
            for chunk in chunks:
                self.assertLessEqual(utf16_length(chunk["content"]), CHUNK_SIZE, name)
                chunk["content"].encode("utf-8")
                self.assertTrue(chunk["content"].strip(), name)
        self.assertEqual(utf16_length("中文🙂"), 4)

    def test_invalid_input_and_large_character_limit(self):
        for text in (None, "bad\x00text", "x" * 2_000_001, "\ud800"):
            with self.assertRaises(ValueError):
                chunk_text(text)
