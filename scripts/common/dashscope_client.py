"""Minimal DashScope HTTP client with no implicit retry or key logging."""
from __future__ import annotations

import json
import os
import urllib.error
import urllib.request
from pathlib import Path


class DashScopeError(RuntimeError):
    pass


class DashScopeClient:
    def __init__(self) -> None:
        self.api_key = os.getenv("DASHSCOPE_API_KEY", "").strip()
        if not self.api_key:
            raise DashScopeError("DASHSCOPE_API_KEY is not configured")
        base = os.getenv("DASHSCOPE_BASE_URL", "https://dashscope.aliyuncs.com/api/v1")
        self.base_url = base.rstrip("/")
        self.timeout = float(os.getenv("DASHSCOPE_TIMEOUT_SECONDS", "180"))

    def generate(self, payload: dict) -> tuple[dict, str]:
        """Perform exactly one model API request. This method never retries."""
        url = self.base_url + "/services/aigc/multimodal-generation/generation"
        request = urllib.request.Request(
            url,
            data=json.dumps(payload, ensure_ascii=False).encode("utf-8"),
            headers={
                "Authorization": f"Bearer {self.api_key}",
                "Content-Type": "application/json",
                "User-Agent": "xiaov2-model-provider/1.0",
            },
            method="POST",
        )
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                raw = response.read()
        except urllib.error.HTTPError as exc:
            detail = exc.read(4096).decode("utf-8", errors="replace")
            raise DashScopeError(f"DashScope HTTP {exc.code}: {detail}") from exc
        except (urllib.error.URLError, TimeoutError) as exc:
            raise DashScopeError(f"DashScope request failed: {exc}") from exc
        try:
            body = json.loads(raw)
        except json.JSONDecodeError as exc:
            raise DashScopeError("DashScope returned invalid JSON") from exc
        request_id = str(body.get("request_id") or "")
        status = int(body.get("status_code") or 200)
        if status != 200 or body.get("code"):
            raise DashScopeError(
                f"DashScope rejected request id={request_id or '-'} "
                f"code={body.get('code') or status}: {body.get('message') or 'unknown error'}"
            )
        return body, request_id

    def download(self, url: str, target: Path) -> None:
        """Download one provider result URL without retrying."""
        if not url.startswith(("https://", "http://")):
            raise DashScopeError("DashScope returned an invalid audio URL")
        request = urllib.request.Request(url, headers={"User-Agent": "xiaov2-model-provider/1.0"})
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                target.write_bytes(response.read())
        except (urllib.error.HTTPError, urllib.error.URLError, TimeoutError) as exc:
            raise DashScopeError(f"DashScope audio download failed: {exc}") from exc

