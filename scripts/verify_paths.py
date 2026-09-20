#!/usr/bin/env python3
"""Print machine-readable canonical paths for cross-language verification."""
import argparse
import json

from common.paths import KINDS, book_root, resource_dir

parser = argparse.ArgumentParser()
parser.add_argument("book_id")
args = parser.parse_args()
print(json.dumps({"book_root": str(book_root(args.book_id)),
                  **{kind: str(resource_dir(args.book_id, kind)) for kind in KINDS}}, sort_keys=True))
