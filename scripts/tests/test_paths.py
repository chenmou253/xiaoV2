import importlib
import os
import tempfile
import unittest
from pathlib import Path


class PathProtocolTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        os.environ["RESOURCE_ROOT"] = self.temp.name
        import common.paths
        self.paths = importlib.reload(common.paths)

    def tearDown(self):
        self.temp.cleanup()
        os.environ.pop("RESOURCE_ROOT", None)

    def test_all_resources_are_isolated_under_book_id(self):
        root = self.paths.book_root("codex-path-verification")
        for kind in self.paths.KINDS:
            self.assertEqual(self.paths.resource_dir("codex-path-verification", kind), root / kind)

    def test_invalid_book_ids_and_traversal_are_rejected(self):
        for value in ("../book", "book/other", "book\\other", "", "."):
            with self.assertRaises(ValueError):
                self.paths.book_root(value)
        with self.assertRaises(ValueError):
            self.paths.relative_path("safe-book", "../../outside")

    def test_page_names_are_stable(self):
        self.assertEqual(self.paths.page_stem(1), "page-001")
        self.assertEqual(self.paths.page_stem(200), "page-200")


if __name__ == "__main__":
    unittest.main()
