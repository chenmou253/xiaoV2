import copy
import json
import os
import types
import unittest
from unittest import mock

from translation_daemon import LocalItemValidationError, parse_sentence_result, parse_word_result

from translate_page import (
    BATCH_REVIEW_SCHEMA,
    BATCH_TRANSLATION_SCHEMA,
    OnlineLLMClient,
    REVIEWER_SYSTEM_PROMPT,
    _page_items,
    clean_translation,
    normalize_source_text,
    page_context,
    parse_batch_review,
    parse_review,
    review_candidate,
    translate_page,
    translate_target,
)


class QueueBackend:
    def __init__(self, responses):
        self.responses = list(responses)
        self.calls = []
        self.structured_calls = []

    def generate_structured(self, system_prompt, user_prompt, max_tokens, *, schema_name, schema):
        self.structured_calls.append((schema_name, schema))
        return self.generate(system_prompt, user_prompt, max_tokens)

    def generate(self, system_prompt, user_prompt, max_tokens):
        self.calls.append((system_prompt, user_prompt, max_tokens))
        response = self.responses.pop(0)
        if isinstance(response, Exception):
            raise response
        return response


def approved(translation, score=0.98):
    return json.dumps(
        {
            "passed": True,
            "score": score,
            "issues": [],
            "corrected_translation": translation,
        },
        ensure_ascii=False,
    )


class TranslationTests(unittest.TestCase):
    def test_local_daemon_sentence_result_is_plain_text_not_json(self):
        self.assertEqual(parse_sentence_result("我看见一家医院。"), "我看见一家医院。")
        with self.assertRaises(LocalItemValidationError):
            parse_sentence_result("翻译：\n我看见一家医院。")

    def test_local_daemon_word_result_requires_exactly_two_lines(self):
        meaning, phonetic = parse_word_result("医院\n/ˈhɑːspɪtl/", "hospital")
        self.assertEqual(meaning, "医院")
        self.assertEqual(phonetic, "ˈhɑːspɪtl")
        with self.assertRaises(LocalItemValidationError):
            parse_word_result("医院", "hospital")
        with self.assertRaises(LocalItemValidationError):
            parse_word_result("医院\nhospital", "hospital")

    def test_local_daemon_rejects_sentence_translation_as_word_meaning(self):
        with self.assertRaises(LocalItemValidationError):
            parse_word_result("这些礼物是给你的。\nfɔːr", "for")
        self.assertEqual(parse_word_result("给\nfɔːr", "for"), ("给", "fɔːr"))

    def test_local_daemon_rejects_non_ipa_word_phonetics(self):
        with self.assertRaises(LocalItemValidationError):
            parse_word_result("听\nr'listen", "listen")
        with self.assertRaises(LocalItemValidationError):
            parse_word_result("听\nr:listen", "listen")
        with self.assertRaises(LocalItemValidationError):
            parse_word_result("听\nlisten", "listen")
        self.assertEqual(parse_word_result("听\nˈlɪsən", "listen"), ("听", "ˈlɪsən"))

    def test_translation_completion_budget_defaults_to_qwen_max_and_is_configurable(self):
        from translate_page import configured_translation_completion_tokens

        with mock.patch.dict(os.environ, {}, clear=True):
            self.assertEqual(configured_translation_completion_tokens(), 131072)
        with mock.patch.dict(
            os.environ, {"TRANSLATION_MAX_COMPLETION_TOKENS": "4096"}, clear=True
        ):
            self.assertEqual(configured_translation_completion_tokens(), 4096)

    def test_page_payload_keeps_sentence_punctuation_but_omits_symbol_words(self):
        segments = [{
            "id": "p1-s1",
            "text": "Hello, world!",
            "words": [
                {"id": "hello", "text": "Hello,"},
                {"id": "comma", "text": ","},
                {"id": "world", "text": "world!"},
            ],
        }]

        items, expected = _page_items(segments)

        self.assertEqual(items[0]["target_text"], "Hello, world!")
        self.assertEqual([word["text"] for word in items[0]["words"]], ["Hello,", "world!"])
        self.assertEqual(expected, {"p1-s1": {"hello", "world"}})

    def test_online_client_does_not_retry_transient_http_error(self):
        class ServiceUnavailable(Exception):
            status_code = 503

        class Completions:
            def __init__(self):
                self.calls = 0

            def create(self, **kwargs):
                self.calls += 1
                raise ServiceUnavailable("temporary")

        completions = Completions()
        backend = object.__new__(OnlineLLMClient)
        backend.model = "test-model"
        backend.temperature = 0.1
        backend.operation_label = "整页翻译"
        backend.request_type = "page_translation"
        backend.page_number = 1
        backend.attempt = 1
        backend.status_callback = None
        backend._extra_body = {"enable_thinking": False}
        backend._stats = {
            "total_requests": 0,
            "total_retries": 0,
            "prompt_tokens": 0,
            "completion_tokens": 0,
            "total_tokens": 0,
            "latency_seconds": 0.0,
            "failed_requests": 0,
            "review_failures": 0,
        }
        backend._client = types.SimpleNamespace(
            chat=types.SimpleNamespace(completions=completions)
        )
        from translate_page import _call_once

        with self.assertRaises(ServiceUnavailable):
            _call_once(
                backend,
                "system",
                "user",
                kind="page_translation",
                page=1,
                parser=lambda value: value,
                max_completion_tokens=32,
            )
        self.assertEqual(completions.calls, 1)

    def test_online_client_does_not_retry_auth_error(self):
        class Unauthorized(Exception):
            status_code = 401

        class Completions:
            calls = 0

            def create(self, **kwargs):
                self.calls += 1
                raise Unauthorized("invalid key")

        completions = Completions()
        backend = object.__new__(OnlineLLMClient)
        backend.model = "test-model"
        backend.temperature = 0.1
        backend.operation_label = "整页翻译"
        backend.request_type = "page_translation"
        backend.page_number = 1
        backend.attempt = 1
        backend.status_callback = None
        backend._extra_body = {"enable_thinking": False}
        backend._stats = {
            "total_requests": 0,
            "total_retries": 0,
            "prompt_tokens": 0,
            "completion_tokens": 0,
            "total_tokens": 0,
            "latency_seconds": 0.0,
            "failed_requests": 0,
            "review_failures": 0,
        }
        backend._client = types.SimpleNamespace(
            chat=types.SimpleNamespace(completions=completions)
        )
        from translate_page import _call_once

        with self.assertRaises(Unauthorized), mock.patch("translate_page.time.sleep") as sleep:
            _call_once(
                backend,
                "system",
                "user",
                kind="page_translation",
                page=1,
                parser=lambda value: value,
                max_completion_tokens=32,
            )
        self.assertEqual(completions.calls, 1)
        sleep.assert_not_called()

    def test_structured_call_disables_thinking_and_uses_strict_json_schema(self):
        class Completions:
            kwargs = None

            def create(self, **kwargs):
                self.kwargs = kwargs
                return types.SimpleNamespace(
                    choices=[types.SimpleNamespace(
                        message=types.SimpleNamespace(content='{"issues":[]}'),
                        finish_reason="stop",
                    )],
                    usage=types.SimpleNamespace(
                        prompt_tokens=25, completion_tokens=4, total_tokens=29
                    ),
                )

        completions = Completions()
        backend = object.__new__(OnlineLLMClient)
        backend.model = "qwen3.7-flash"
        backend.temperature = 0.1
        backend.request_type = "page_review"
        backend.page_number = 2
        backend.attempt = 1
        backend._extra_body = {"enable_thinking": False}
        backend._stats = {
            "total_requests": 0,
            "total_retries": 0,
            "prompt_tokens": 0,
            "completion_tokens": 0,
            "total_tokens": 0,
            "latency_seconds": 0.0,
            "failed_requests": 0,
            "review_failures": 0,
        }
        backend._client = types.SimpleNamespace(
            chat=types.SimpleNamespace(completions=completions)
        )
        backend.generate_structured(
            "system", "review JSON", 512,
            schema_name="page_review",
            schema=BATCH_REVIEW_SCHEMA,
        )
        kwargs = completions.kwargs
        self.assertEqual(kwargs["extra_body"], {"enable_thinking": False})
        self.assertEqual(kwargs["response_format"]["type"], "json_schema")
        self.assertTrue(kwargs["response_format"]["json_schema"]["strict"])
        self.assertFalse(kwargs["response_format"]["json_schema"]["schema"]["additionalProperties"])
        self.assertEqual(kwargs["max_completion_tokens"], 512)
        self.assertNotIn("max_tokens", kwargs)

    def test_connect_and_read_timeouts_are_configured_separately(self):
        from openai import OpenAI

        with mock.patch("openai.OpenAI", return_value=object()) as make_client:
            OnlineLLMClient(
                base_url="https://dashscope.aliyuncs.com/compatible-mode/v1",
                api_key="test-only",
                model="qwen3.7-flash",
                connect_timeout=7,
                read_timeout=45,
            )
        timeout = make_client.call_args.kwargs["timeout"]
        self.assertEqual(timeout.connect, 7)
        self.assertEqual(timeout.read, 45)

    def test_translation_logs_do_not_dump_text_by_default(self):
        from contextlib import redirect_stderr
        from io import StringIO
        from translate_page import TranslationResult, log_translation

        output = StringIO()
        with redirect_stderr(output), mock.patch.dict(os.environ, {}, clear=False):
            os.environ.pop("TRANSLATION_DEBUG", None)
            log_translation(
                kind="sentence",
                target="A private sample sentence.",
                context="The entire paragraph is private.",
                result=TranslationResult("一条译文。", None, [], "PASS"),
            )
        self.assertNotIn("A private sample sentence", output.getvalue())
        self.assertNotIn("The entire paragraph is private", output.getvalue())

    def test_batch_does_not_retry_permission_error(self):
        class Forbidden(Exception):
            status_code = 403

        backend = QueueBackend([Forbidden("model not enabled")])
        from translate_page import _batch_generate

        with self.assertRaises(Forbidden):
            _batch_generate(
                backend,
                "system",
                "page",
                kind="page_translation",
                stage_label="整页翻译",
                parser=lambda value: value,
            )
        self.assertEqual(len(backend.calls), 1)

    def test_batch_does_not_retry_transient_error(self):
        backend = QueueBackend([TimeoutError("read timed out"), "ok"])
        from translate_page import _batch_generate

        with self.assertRaises(TimeoutError):
            result = _batch_generate(
                backend,
                "system",
                "page",
                kind="page_translation",
                stage_label="整页翻译",
                parser=lambda value: value,
            )
        self.assertEqual(len(backend.calls), 1)

    def test_clean_translation_removes_wrappers_not_punctuation(self):
        raw = "```text\n> 翻译结果：你好，汤姆！\n```"
        self.assertEqual(clean_translation(raw), "你好，汤姆！")

    def test_normalize_source_collapses_ocr_line_wrap(self):
        self.assertEqual(
            normalize_source_text("Doctors are great! My\nfather is a doctor too!"),
            "Doctors are great! My father is a doctor too!",
        )

    def test_reviewer_json_code_fence_is_parsed(self):
        result = parse_review(
            """```json
{"passed":false,"score":0.72,"issues":["错译"],"corrected_translation":"我喜欢苹果。"}
```""",
            "我讨厌苹果。",
        )
        self.assertFalse(result.passed)
        self.assertEqual(result.corrected_translation, "我喜欢苹果。")

    def test_reviewer_ignores_trailing_json_object(self):
        result = parse_review(
            '{"passed":true,"score":0.98,"issues":[],"corrected_translation":"你好。"}'
            '{"debug":"ignored"}',
            "你好。",
        )
        self.assertTrue(result.passed)
        self.assertEqual(result.corrected_translation, "你好。")

    def test_reviewer_correction_overrides_inconsistent_pass_flag(self):
        result = parse_review(approved("我喜欢苹果。"), "我讨厌苹果。")
        self.assertFalse(result.passed)
        self.assertIn("reviewer corrected the candidate", result.issues)

    def test_structured_json_parser_handles_escaped_quotes_slashes_and_newlines(self):
        from translate_page import _clean_json_payload

        value = json.dumps(
            {"issues": [], "text": '他说“go\\now”，然后说 "stop"。\n下一行'},
            ensure_ascii=False,
        )
        self.assertEqual(json.loads(_clean_json_payload(value))["text"], json.loads(value)["text"])

    def test_malformed_translation_json_is_returned_without_retry(self):
        from translate_page import _batch_generate, parse_batch_translation

        backend = QueueBackend(['{"segments":[{"id":"s1" "translation":"你好。","words":[]}]}'])
        with self.assertRaises(json.JSONDecodeError):
            _batch_generate(
                backend,
                "translate",
                "page",
                kind="page_translation",
                stage_label="整页翻译",
                parser=lambda raw: parse_batch_translation(raw, {"s1": set()}),
                schema=BATCH_TRANSLATION_SCHEMA,
                page=2,
                max_completion_tokens=512,
            )
        self.assertEqual(len(backend.calls), 1)

    def test_empty_word_meaning_is_returned_without_retry(self):
        from translate_page import _batch_generate, parse_batch_translation

        response = json.dumps(
            {
                "segments": [{
                    "id": "s1",
                    "translation": "这是句子。",
                    "words": [{"id": "w4", "meaning": "", "phonetic": "/test/"}],
                }]
            },
            ensure_ascii=False,
        )
        backend = QueueBackend([response, response])
        with self.assertRaisesRegex(ValueError, "empty meaning: w4"):
            _batch_generate(
                backend,
                "translate",
                "page",
                kind="page_translation",
                stage_label="整页翻译",
                parser=lambda raw: parse_batch_translation(raw, {"s1": {"w4"}}),
                schema=BATCH_TRANSLATION_SCHEMA,
                page=4,
                max_completion_tokens=512,
            )
        self.assertEqual(len(backend.calls), 1)

    def test_explicit_context_wins_over_page_window(self):
        segments = [
            {"text": "Unrelated."},
            {"text": "bank", "paragraph": "We sat on the river bank."},
        ]
        self.assertEqual(page_context(segments, 1), "We sat on the river bank.")

    def test_target_is_appended_when_explicit_context_omits_it(self):
        segments = [{"text": "My father is a doctor too!", "context": "Doctors are great!"}]
        self.assertEqual(
            page_context(segments, 0),
            "Doctors are great!\nMy father is a doctor too!",
        )

    def test_sentence_translation_uses_context_and_reviewer_correction(self):
        backend = QueueBackend(
            [
                "我讨厌苹果。",
                approved("我喜欢苹果。", score=0.99),
            ]
        )
        result = translate_target(
            backend,
            kind="sentence",
            target="I like apples.",
            context="I like apples.",
        )
        self.assertEqual(result.translation, "我喜欢苹果。")
        self.assertEqual(result.review, "WARNING")
        self.assertNotIn("CONTEXT:", backend.calls[0][1])
        self.assertIs(backend.calls[1][0], REVIEWER_SYSTEM_PROMPT)

    def test_word_translation_is_scoped_to_sentence(self):
        backend = QueueBackend(["河岸", approved("河岸")])
        result = translate_target(
            backend,
            kind="word",
            target="bank",
            context="We sat on the river bank.",
            sentence="We sat on the river bank.",
        )
        self.assertEqual(result.translation, "河岸")
        self.assertNotIn("CONTEXT:", backend.calls[0][1])
        self.assertIn("TARGET SENTENCE:\nWe sat on the river bank.", backend.calls[0][1])
        self.assertIn("TARGET WORD:\nbank", backend.calls[0][1])

    def test_reviewer_does_not_retry_malformed_json(self):
        backend = QueueBackend(["not json", "still not json", approved("他不喜欢足球。")])
        result = review_candidate(
            backend,
            kind="sentence",
            target="He doesn't like football.",
            context="He doesn't like football.",
            candidate="他喜欢足球。",
        )
        self.assertEqual(len(backend.calls), 1)
        self.assertTrue(result.reviewer_failed)
        self.assertEqual(result.corrected_translation, "他喜欢足球。")

    def test_reviewer_failure_keeps_candidate_and_marks_failure(self):
        backend = QueueBackend(["bad", "bad", "bad"])
        result = review_candidate(
            backend,
            kind="sentence",
            target="Hello!",
            context="Hello!",
            candidate="你好！",
        )
        self.assertTrue(result.reviewer_failed)
        self.assertEqual(result.corrected_translation, "你好！")

    def test_page_schema_preserves_existing_phonetic(self):
        original = {
            "page": 1,
            "reviewed": False,
            "segments": [
                {
                    "id": "s1",
                    "text": "Hello!",
                    "translation": "旧翻译",
                    "anchor": [0.1, 0.2, 0.3, 0.1],
                    "words": [
                        {
                            "id": "w1",
                            "text": "Hello",
                            "meaning": "旧词义",
                            "phonetic": "həˈloʊ",
                            "box": [0.1, 0.2, 0.1, 0.1],
                        }
                    ],
                }
            ],
        }
        backend = QueueBackend(
            [
                json.dumps(
                    {
                        "segments": [
                            {
                                "id": "s1",
                                "translation": "你好！",
                                "words": [{"id": "w1", "meaning": "你好", "phonetic": "hɛˈloʊ"}],
                            }
                        ]
                    },
                    ensure_ascii=False,
                ),
                json.dumps(
                    {"issues": []},
                    ensure_ascii=False,
                ),
            ]
        )
        result = translate_page(copy.deepcopy(original), backend)
        segment = result["segments"][0]
        self.assertEqual(segment["translation"], "你好！")
        self.assertEqual(segment["words"][0]["meaning"], "你好")
        self.assertEqual(segment["words"][0]["phonetic"], "həˈloʊ")
        self.assertEqual(segment["anchor"], original["segments"][0]["anchor"])
        self.assertEqual(segment["words"][0]["box"], original["segments"][0]["words"][0]["box"])
        self.assertEqual(len(backend.calls), 2)
        self.assertEqual(backend.calls[0][2], 131072)
        self.assertEqual(
            [name for name, _ in backend.structured_calls],
            ["page_translation", "page_review"],
        )
        self.assertFalse(backend.structured_calls[0][1]["additionalProperties"])

    def test_page_fills_missing_phonetic_from_model(self):
        original = {
            "page": 1,
            "segments": [
                {
                    "id": "s1",
                    "text": "Hello!",
                    "words": [{"id": "w1", "text": "Hello", "phonetic": ""}],
                }
            ],
        }
        backend = QueueBackend(
            [
                json.dumps(
                    {
                        "segments": [
                            {
                                "id": "s1",
                                "translation": "你好！",
                                "words": [
                                    {"id": "w1", "meaning": "你好", "phonetic": "hɛˈloʊ"}
                                ],
                            }
                        ]
                    },
                    ensure_ascii=False,
                ),
                json.dumps(
                    {"issues": []},
                    ensure_ascii=False,
                ),
            ]
        )
        result = translate_page(copy.deepcopy(original), backend)
        self.assertEqual(result["segments"][0]["words"][0]["phonetic"], "hɛˈloʊ")
        self.assertIn('"phonetic":""', backend.calls[0][1])
        self.assertIn("General American English IPA", backend.calls[0][0])
        self.assertIn("rhotic American pronunciation", backend.calls[0][0])
        self.assertIn("General American English IPA", backend.calls[1][0])
        self.assertIn("British/non-rhotic variants", backend.calls[1][0])

    def test_batch_parser_rejects_missing_ids(self):
        from translate_page import parse_batch_translation

        with self.assertRaises(ValueError):
            parse_batch_translation(
                '{"segments":[{"id":"wrong","translation":"你好","words":[]}]}'
                , {"s1": set()}
            )

    def test_page_review_returns_only_corrections_and_overlays_them(self):
        candidates = {
            "s1": {
                "translation": "我喜欢苹果。",
                "words": {"w1": {"meaning": "苹果", "phonetic": "ˈæpəl"}},
            }
        }
        raw = json.dumps(
            {
                "issues": [
                    {
                        "segment_id": "s1",
                        "word_id": "",
                        "field": "translation",
                        "reason": "遗漏否定",
                        "suggestion": "我不喜欢苹果。",
                    },
                    {
                        "segment_id": "s1",
                        "word_id": "w1",
                        "field": "meaning",
                        "reason": "词义错误",
                        "suggestion": "果实",
                    },
                ]
            },
            ensure_ascii=False,
        )
        result = parse_batch_review(raw, candidates, {"s1": {"w1"}})
        self.assertEqual(result["s1"]["translation"], "我不喜欢苹果。")
        self.assertEqual(result["s1"]["words"]["w1"]["meaning"], "果实")
        self.assertFalse(result["s1"]["passed"])

    def test_page_review_failure_keeps_completed_translation(self):
        original = {
            "page": 2,
            "segments": [{"id": "s1", "text": "Hello!", "words": []}],
        }
        translated = json.dumps(
            {"segments": [{"id": "s1", "translation": "你好！", "words": []}]},
            ensure_ascii=False,
        )
        backend = QueueBackend([translated, "bad"])
        result = translate_page(copy.deepcopy(original), backend)
        self.assertEqual(result["segments"][0]["translation"], "你好！")
        self.assertEqual(len(backend.calls), 2)
        self.assertEqual(
            [name for name, _ in backend.structured_calls],
            ["page_translation", "page_review"],
        )



    def test_local_prompt_does_not_expose_ids(self):
        from translate_page import local_translation_prompt

        items = [{
            "id": "p54-s0",
            "context": "Hello world.",
            "target_text": "Hello world.",
            "words": [
                {"id": "p54-s0-w0", "text": "Hello", "phonetic": ""},
                {"id": "p54-s0-w1", "text": "world", "phonetic": ""},
            ],
        }]
        prompt = local_translation_prompt(items)
        self.assertNotIn("p54-s0", prompt)
        self.assertNotIn("p54-s0-w0", prompt)
        self.assertIn("Hello world.", prompt)
        self.assertIn('"text":"Hello"', prompt)

    def test_local_candidate_validation_helpers(self):
        from translate_page import expected_for_items, validate_complete_candidates

        items = [{
            "id": "s0",
            "target_text": "Hello",
            "context": "",
            "words": [{"id": "w0", "text": "Hello", "phonetic": ""}],
        }]
        expected = expected_for_items(items)
        candidates = {
            "s0": {
                "translation": "你好",
                "words": {"w0": {"meaning": "你好", "phonetic": "həˈloʊ"}},
            }
        }
        validate_complete_candidates(candidates, expected)
        self.assertEqual(expected, {"s0": {"w0"}})

    def test_local_translation_batches_are_fixed_at_one_segment(self):
        from translate_page import _local_item_batches

        items = [{"id": f"s{index}", "target_text": str(index), "context": "", "words": []}
                 for index in range(7)]
        batches = _local_item_batches(items)
        self.assertEqual([len(batch) for batch in batches], [1, 1, 1, 1, 1, 1, 1])
        self.assertEqual(
            [[item["id"] for item in batch] for batch in batches],
            [["s0"], ["s1"], ["s2"], ["s3"], ["s4"], ["s5"], ["s6"]],
        )

    def test_local_translation_prompt_sends_no_context(self):
        from translate_page import local_translation_prompt

        items = [
            {
                "id": "s0",
                "context": "ignored repeated page context",
                "target_text": "First sentence.",
                "words": [{"id": "w0", "text": "First", "phonetic": ""}],
            },
            {
                "id": "s1",
                "context": "ignored repeated page context",
                "target_text": "Second sentence.",
                "words": [{"id": "w1", "text": "Second", "phonetic": ""}],
            },
        ]
        prompt = local_translation_prompt(items)
        source_payload = prompt.split("SOURCE INPUT:\n", 1)[1].split("\n\nOUTPUT TEMPLATE:", 1)[0]
        payload = json.loads(source_payload)
        self.assertNotIn("context", payload)
        self.assertNotIn("context", payload["items"][0])
        self.assertNotIn("context", payload["items"][1])
        self.assertEqual(payload["items"][0]["target_text"], "First sentence.")
        self.assertEqual(payload["items"][1]["target_text"], "Second sentence.")

    def test_local_translation_maps_results_back_by_position(self):
        from translate_page import parse_local_translation

        items = [{
            "id": "p54-s0",
            "context": "Hello world.",
            "target_text": "Hello world.",
            "words": [
                {"id": "p54-s0-w0", "text": "Hello", "phonetic": ""},
                {"id": "p54-s0-w1", "text": "world", "phonetic": ""},
            ],
        }]
        raw = json.dumps(
            {
                "segments": [{
                    "translation": "你好，世界。",
                    "words": [
                        {"meaning": "你好", "phonetic": "həˈloʊ"},
                        {"meaning": "世界", "phonetic": "wɝːld"},
                    ],
                }]
            },
            ensure_ascii=False,
        )
        parsed = parse_local_translation(raw, items)
        self.assertEqual(parsed["p54-s0"]["translation"], "你好，世界。")
        self.assertEqual(parsed["p54-s0"]["words"]["p54-s0-w0"]["meaning"], "你好")
        self.assertEqual(parsed["p54-s0"]["words"]["p54-s0-w1"]["meaning"], "世界")

    def test_local_translation_prompt_contains_exact_output_skeleton(self):
        from translate_page import local_translation_prompt

        items = [{
            "id": "p54-s0",
            "context": "Listen and repeat.",
            "target_text": "Listen and repeat.",
            "words": [
                {"id": "w0", "text": "Listen", "phonetic": ""},
                {"id": "w1", "text": "and", "phonetic": ""},
                {"id": "w2", "text": "repeat", "phonetic": ""},
            ],
        }]
        prompt = local_translation_prompt(items)
        self.assertIn("OUTPUT TEMPLATE:", prompt)
        template_text = prompt.split("OUTPUT TEMPLATE:\n", 1)[1]
        template = json.loads(template_text)
        self.assertEqual(len(template["segments"]), 1)
        self.assertEqual(len(template["segments"][0]["words"]), 3)
        self.assertEqual(
            template["segments"][0]["words"],
            [
                {"meaning": "", "phonetic": ""},
                {"meaning": "", "phonetic": ""},
                {"meaning": "", "phonetic": ""},
            ],
        )

    def test_local_translation_rejects_prose_wrapped_or_inner_json(self):
        from translate_page import parse_local_translation

        items = [{
            "id": "p54-s0",
            "context": "Hello.",
            "target_text": "Hello.",
            "words": [{"id": "w0", "text": "Hello", "phonetic": ""}],
        }]
        raw = 'prefix {"segments":[{"translation":"你好","words":[{"meaning":"你好","phonetic":"həˈloʊ"}]}]} suffix'
        with self.assertRaises(json.JSONDecodeError):
            parse_local_translation(raw, items)

    def test_local_translation_accepts_empty_word_placeholders(self):
        from translate_page import parse_local_translation

        items = [{
            "id": "p54-s0",
            "context": "Hello world.",
            "target_text": "Hello world.",
            "words": [
                {"id": "p54-s0-w0", "text": "Hello", "phonetic": ""},
                {"id": "p54-s0-w1", "text": "world", "phonetic": ""},
            ],
        }]
        raw = json.dumps(
            {
                "segments": [{
                    "translation": "你好，世界。",
                    "words": [
                        {"meaning": "你好", "phonetic": "həˈloʊ"},
                        {"meaning": "", "phonetic": ""},
                    ],
                }]
            },
            ensure_ascii=False,
        )
        parsed = parse_local_translation(raw, items)
        self.assertEqual(parsed["p54-s0"]["words"]["p54-s0-w0"]["meaning"], "你好")
        self.assertEqual(parsed["p54-s0"]["words"]["p54-s0-w1"]["meaning"], "")
        self.assertEqual(parsed["p54-s0"]["words"]["p54-s0-w1"]["phonetic"], "")

    def test_local_translation_rejects_position_count_mismatch(self):
        from translate_page import parse_local_translation

        items = [{
            "id": "p54-s0",
            "context": "Hello world.",
            "target_text": "Hello world.",
            "words": [
                {"id": "p54-s0-w0", "text": "Hello", "phonetic": ""},
                {"id": "p54-s0-w1", "text": "world", "phonetic": ""},
            ],
        }]
        raw = json.dumps(
            {
                "segments": [{
                    "translation": "你好，世界。",
                    "words": [{"meaning": "你好", "phonetic": "həˈloʊ"}],
                }]
            },
            ensure_ascii=False,
        )
        with self.assertRaisesRegex(ValueError, "word count mismatch"):
            parse_local_translation(raw, items)

    def test_local_review_uses_indexes_not_ids(self):
        from translate_page import local_review_prompt, parse_local_review

        items = [{
            "id": "p54-s0",
            "context": "I like apples.",
            "target_text": "I like apples.",
            "words": [{"id": "p54-s0-w0", "text": "apples", "phonetic": ""}],
        }]
        candidates = {
            "p54-s0": {
                "translation": "我喜欢苹果。",
                "words": {"p54-s0-w0": {"meaning": "苹果", "phonetic": "ˈæpəlz"}},
            }
        }
        prompt = local_review_prompt(items, candidates)
        self.assertNotIn("p54-s0", prompt)
        raw = json.dumps(
            {
                "issues": [{
                    "segment_index": 0,
                    "word_index": 0,
                    "field": "meaning",
                    "reason": "词义需更准确",
                    "suggestion": "苹果（复数）",
                }]
            },
            ensure_ascii=False,
        )
        reviewed = parse_local_review(raw, items, candidates)
        self.assertEqual(
            reviewed["p54-s0"]["words"]["p54-s0-w0"]["meaning"],
            "苹果（复数）",
        )

    def test_local_structured_call_retries_malformed_json_once(self):
        from translate_page import (
            LOCAL_TRANSLATION_SCHEMA,
            LocalMLXBackend,
            _local_structured_call,
        )

        backend = object.__new__(LocalMLXBackend)
        backend.model = "local-qwen3-4b-instruct-2507"
        backend.operation_label = "本地翻译"
        backend.request_type = "page_translation"
        backend.page_number = 7
        backend.attempt = 1
        backend.status_callback = None
        responses = iter([
            '{"segments":[{"translation":"你好" "words":[]}]}',
            '{"segments":[{"translation":"你好","words":[]}]}',
        ])

        def generate_structured(system_prompt, user_prompt, max_completion_tokens, *, schema_name, schema):
            del system_prompt, user_prompt, max_completion_tokens, schema_name, schema
            return next(responses)

        backend.generate_structured = generate_structured
        calls = {"count": 0}

        def parser(raw):
            calls["count"] += 1
            return json.loads(raw)

        result = _local_structured_call(
            backend,
            "system",
            "prompt",
            kind="page_translation",
            page=7,
            parser=parser,
            schema=LOCAL_TRANSLATION_SCHEMA,
            max_completion_tokens=512,
        )
        self.assertEqual(result["segments"][0]["translation"], "你好")
        self.assertEqual(calls["count"], 2)

    def test_local_structured_call_stops_after_second_json_failure(self):
        from translate_page import (
            LOCAL_TRANSLATION_SCHEMA,
            LocalMLXBackend,
            _local_structured_call,
        )

        backend = object.__new__(LocalMLXBackend)
        backend.model = "local-qwen3-4b-instruct-2507"
        backend.operation_label = "本地翻译"
        backend.request_type = "page_translation"
        backend.page_number = 7
        backend.attempt = 1
        backend.status_callback = None
        calls = {"count": 0}

        def generate_structured(system_prompt, user_prompt, max_completion_tokens, *, schema_name, schema):
            del system_prompt, user_prompt, max_completion_tokens, schema_name, schema
            calls["count"] += 1
            return '{"segments":[{"translation":"坏掉" "words":[]}]}'

        backend.generate_structured = generate_structured
        with self.assertRaises(json.JSONDecodeError):
            _local_structured_call(
                backend,
                "system",
                "prompt",
                kind="page_translation",
                page=7,
                parser=json.loads,
                schema=LOCAL_TRANSLATION_SCHEMA,
                max_completion_tokens=512,
            )
        self.assertEqual(calls["count"], 2)

    def test_local_structured_call_retries_structure_mismatch_once_without_splitting(self):
        from translate_page import (
            LOCAL_TRANSLATION_SCHEMA,
            LocalMLXBackend,
            LocalStructureError,
            _local_structured_call,
        )

        backend = object.__new__(LocalMLXBackend)
        backend.model = "local-qwen3-4b-instruct-2507"
        backend.operation_label = "本地翻译"
        backend.request_type = "page_translation"
        backend.page_number = 7
        backend.attempt = 1
        backend.status_callback = None
        calls = {"count": 0}

        def generate_structured(system_prompt, user_prompt, max_completion_tokens, *, schema_name, schema):
            del system_prompt, user_prompt, max_completion_tokens, schema_name, schema
            calls["count"] += 1
            return '{"segments":[]}'

        backend.generate_structured = generate_structured
        with self.assertRaisesRegex(LocalStructureError, "count mismatch"):
            _local_structured_call(
                backend,
                "system",
                "prompt",
                kind="page_translation",
                page=7,
                parser=lambda raw: (_ for _ in ()).throw(LocalStructureError("count mismatch")),
                schema=LOCAL_TRANSLATION_SCHEMA,
                max_completion_tokens=512,
            )
        self.assertEqual(calls["count"], 2)


    def test_batch_translation_accepts_leading_zero_id_variants(self):
        from translate_page import parse_batch_translation

        expected = {"p53-s12": {"p53-s12-w2"}}
        raw = json.dumps(
            {
                "segments": [
                    {
                        "id": "p053-s012",
                        "translation": "电话",
                        "words": [
                            {
                                "id": "p053-s012-w002",
                                "meaning": "电话",
                                "phonetic": "foʊn",
                            }
                        ],
                    }
                ]
            },
            ensure_ascii=False,
        )
        parsed = parse_batch_translation(raw, expected)
        self.assertIn("p53-s12", parsed)
        self.assertIn("p53-s12-w2", parsed["p53-s12"]["words"])

    def test_batch_translation_rejects_real_id_change(self):
        from translate_page import parse_batch_translation

        expected = {"p53-s12": {"p53-s12-w2"}}
        raw = json.dumps(
            {
                "segments": [
                    {
                        "id": "p53-s12",
                        "translation": "电话",
                        "words": [
                            {
                                "id": "p53-s12-w3",
                                "meaning": "电话",
                                "phonetic": "foʊn",
                            }
                        ],
                    }
                ]
            },
            ensure_ascii=False,
        )
        with self.assertRaises(ValueError):
            parse_batch_translation(raw, expected)

if __name__ == "__main__":
    unittest.main()


