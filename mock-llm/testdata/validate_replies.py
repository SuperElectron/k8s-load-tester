"""Check the mock's replies against the pipeline's own parsers, over HTTP.

Sends every prompt in prompts.json through the official openai SDK to a
running mock and feeds the answer to the code the pipeline runs on it:
the Pydantic schema (extract), build_giudizio (legal), normalize_rules
(rules), parse_segments (split), _SUPPORTED_TYPES (classify). Then checks
that 429 and 402 come back as the pipeline's rate-limit errors.

    go run . -addr :18080 &
    cd <llm-backend>/pipeline
    PYTHONPATH=. .venv/bin/python <k8s-load-tester>/mock-llm/testdata/validate_replies.py http://localhost:18080/v1
"""
import json
import os
import subprocess
import sys
import time
from pathlib import Path

for key, value in {
    "ORIENT_MAX_PAGES": "10", "ORIENT_PAGES": "0", "PIPELINE_MAX_RUNS": "3",
    "PIPELINE_ENV": "test", "SITE_ZIP_COMPANY_CONCURRENCY": "1",
    "PIPELINE_HIGH_PRIORITY_MAX_DOCS": "10", "AWS_REGION": "eu-south-1",
}.items():
    os.environ.setdefault(key, value)

from openai import APIStatusError, OpenAI  # noqa: E402

from workflows.doc_extract.steps import classify, extract, legal  # noqa: E402
from workflows.doc_extract.utils import llm, rules, split  # noqa: E402
from workflows.doc_extract.utils.legal import build_giudizio  # noqa: E402
from workflows.doc_extract.utils.schemas import get_schema_for_document  # noqa: E402

base_url = sys.argv[1]
client = OpenAI(base_url=base_url, api_key="mock", max_retries=0)
prompts = json.loads((Path(__file__).parent / "prompts.json").read_text())
failures = []


def ask(p, headers=None):
    resp = client.chat.completions.create(
        model="mock/model", extra_headers=headers or {},
        messages=[{"role": "system", "content": p["system"]},
                  {"role": "user", "content": [{"type": "text", "text": p["user"]}]}],
    )
    return resp.choices[0].message.content


for p in prompts:
    step, doc_type = p["step"], p.get("doc_type")
    text = ask(p)
    try:
        if step == "classify":
            assert text in classify._SUPPORTED_TYPES, text
        elif step == "extract":
            get_schema_for_document(doc_type)(**extract._parse_json_response(text))
        elif step == "legal":
            g = build_giudizio(extract._parse_json_response(text)["elementi"], legal._CHECKLISTS[doc_type])
            assert g["elementi"], g
        elif step == "rules":
            assert rules.normalize_rules(extract._parse_json_response(text)), text
        elif step == "split":
            assert split.parse_segments(text, 7) == [(1, 7)], text
    except Exception as exc:  # noqa: BLE001 — AssertionError, ValidationError, parse errors
        failures.append(f"{step}/{doc_type}: {exc!r}")
print(f"{len(prompts)} prompts checked, {len(failures)} failures")

for status, flag in ((429, "-rate-429"), (402, "-rate-402")):
    port = 18100 + status
    proc = subprocess.Popen(["go", "run", ".", "-addr", f":{port}", flag, "1", "-retry-after-s", "2"],
                            cwd=Path(__file__).parent.parent, stdout=subprocess.DEVNULL)
    try:
        c = OpenAI(base_url=f"http://localhost:{port}/v1", api_key="mock", max_retries=0)
        for _ in range(50):
            try:
                c.chat.completions.create(model="m", messages=[{"role": "system", "content": prompts[0]["system"]}])
            except APIStatusError as exc:
                limited = llm._rate_limit_of(exc)
                if limited is None:
                    failures.append(f"{status}: not seen as a rate limit: {exc}")
                else:
                    print(f"{status}: pipeline sees {type(limited).__name__} {vars(limited)}")
                break
            except Exception:  # noqa: BLE001 — not listening yet
                time.sleep(0.2)
    finally:
        proc.terminate()
        proc.wait()

for f in failures:
    print("FAIL", f)
sys.exit(1 if failures else 0)
