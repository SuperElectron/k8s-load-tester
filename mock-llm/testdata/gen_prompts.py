"""Dump the pipeline's real system/user prompts to prompts.json.

The Go tests detect the step and build each reply from these, so a prompt
change in the pipeline shows up as a failing test after regenerating.

    cd <llm-backend>/pipeline
    PYTHONPATH=. .venv/bin/python <k8s-load-tester>/mock-llm/testdata/gen_prompts.py > <k8s-load-tester>/mock-llm/testdata/prompts.json
"""
import json
import os

# Module-level reads in the pipeline; values do not affect the prompts.
for key, value in {
    "ORIENT_MAX_PAGES": "10", "ORIENT_PAGES": "0", "PIPELINE_MAX_RUNS": "3",
    "PIPELINE_ENV": "test", "SITE_ZIP_COMPANY_CONCURRENCY": "1",
    "PIPELINE_HIGH_PRIORITY_MAX_DOCS": "10", "AWS_REGION": "eu-south-1",
}.items():
    os.environ.setdefault(key, value)

from workflows.doc_extract.steps import classify, entity_resolver, extract, legal  # noqa: E402
from workflows.doc_extract.utils import rules, split  # noqa: E402

out = [
    {"step": "classify", "system": classify._CLASSIFICATION_PROMPT, "user": "Classify this document type:"},
    {"step": "classify_second_opinion", "system": classify._SECOND_OPINION_PROMPT, "user": "attestato or nomina?"},
    {"step": "classify_short_plan", "system": classify._SHORT_PLAN_PROMPT, "user": "accettazione, piano or altro?"},
    {"step": "split", "system": split.SEGMENT_SYSTEM_PROMPT, "user": split.SEGMENT_USER_PROMPT},
    {"step": "id_expiry", "system": extract._ID_EXPIRY_PROMPT, "user": "Expiry date?"},
]
for doc_type in sorted(extract._SUPPORTED_TYPES):
    system, user = extract._build_prompt(doc_type)
    out.append({"step": "extract", "doc_type": doc_type, "system": system, "user": user})
for doc_type, checklist in sorted(legal._CHECKLISTS.items()):
    system, user = legal._build_legal_prompt(checklist)
    out.append({"step": "legal", "doc_type": doc_type, "system": system, "user": user})
system, user = rules.build_rules_prompt({"lavorazioni": ["scavi"], "rischi_particolari": ["caduta dall'alto"]})
out.append({"step": "rules", "doc_type": "psc", "system": system, "user": user})

captured = {}
entity_resolver.call_openrouter = lambda model, messages, **kw: captured.setdefault("m", messages) and "NONE"
entity_resolver._llm_match_entity(subject_type="worker", extracted_label="MARIO VERDI", doc_type="unilav",
                                  candidates=[{"id": "00000000-0000-0000-0000-000000000001", "label": "MARIO VERDI"}])
out.append({"step": "entity_resolver", "system": captured["m"][0]["content"], "user": captured["m"][1]["content"]})

print(json.dumps(out, ensure_ascii=False, indent=1))
