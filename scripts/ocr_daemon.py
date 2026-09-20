#!/usr/bin/env python3
"""Compatibility entrypoint for the long-lived page OCR worker."""
from prepare_book import daemon


if __name__ == "__main__":
    daemon()
