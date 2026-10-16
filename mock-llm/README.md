# mock-llm

An OpenAI-compatible `POST /v1/chat/completions` that answers the document
pipeline's prompts (classify, split, extract, legal, rules, and the small
follow-up questions) with canned JSON that the pipeline accepts, after a
configurable delay and with configurable failures. Load runs through it cost
nothing and give the same answers every time.

| Endpoint | |
|---|---|
| `POST /v1/chat/completions` (also `/chat/completions`) | non-streaming only; `stream: true` is a 400 |
| `GET /healthz` | `ok` |
| `GET /metrics` | Prometheus |

Any `Authorization` header is accepted.

## How a reply is chosen

The step is recognised from a phrase of its **system prompt** (`stepMarkers`
in `reply.go`, first hit wins):

| Step | Marker | Reply |
|---|---|---|
| `split` | `{"documenti": [{"pagina_inizio"` | one document, page 1 to the last page counted in the PDF (the pipeline rebuilds the end page itself) |
| `legal` | `Valuta ESATTAMENTE questi elementi` | every element of the checklist embedded in the prompt, `esito: conforme` |
| `rules` | `{"regole": [` | one rule, `target_doc_type: pos`, `engine: deterministico`, `severity: warning` |
| `classify` | `You are an expert in classifying` | a doc type, see below |
| `classify_second_opinion` | `'attestato' if it CERTIFIES` | `nomina` (keeps the first verdict) |
| `classify_short_plan` | `'accettazione' if it is the front page` | `piano` (keeps the first verdict) |
| `id_expiry` | `You read ONE date on an identity document` | `NONE` |
| `entity_resolver` | `precise entity matcher` | `NONE` (no match) |
| `extract` | `You are an expert in extracting data from` | an object built from the JSON schema embedded in the prompt |

A prompt with no marker gets a 400 (`unrecognised prompt`) and counts as
`step="unknown"`: a pipeline prompt that changed shows up instead of being
answered wrongly.

**Classify** answers, in order of precedence:
1. the `X-Mock-Doc-Type` request header;
2. `mocktype-<doc_type>` in the attached PDF's filename (e.g. upload
   `mocktype-unilav-0001.pdf`);
3. a weighted draw from `-doc-types` (default `durc`).

**Extract** reads the Pydantic JSON schema that follows `in JSON format:` in
the prompt and fills every field: strings `mock <field name>`, integers `1`,
booleans `true`, arrays with one item, nested `$ref` models filled the same
way. Date-named fields (`data_*`, `*scadenza*`, `prossima_*`, `validita*`) get
ISO dates: expiries one year ahead, other dates one month back, so documents
come out valid and not expiring. Because the schema comes from the prompt, a
new doc type needs no change here.

Every 200 carries a `usage` block: prompt tokens ≈ text/4 + 258 per image +
1500 per PDF page; completion tokens ≈ reply/4.

## Configuration

Flags, each with an environment twin (the flag wins).

| Flag | Env | Default | |
|---|---|---|---|
| `-addr` | `MOCK_ADDR` | `:8080` | listen address |
| `-latency` | `MOCK_LATENCY` | `fixed` | `fixed`, `uniform` or `lognormal` |
| `-latency-ms` | `MOCK_LATENCY_MS` | `0` | fixed delay |
| `-latency-min-ms` / `-latency-max-ms` | `MOCK_LATENCY_MIN_MS` / `MOCK_LATENCY_MAX_MS` | `0` | uniform bounds; for lognormal, max is a cap (0 = none) |
| `-latency-median-ms` | `MOCK_LATENCY_MEDIAN_MS` | `0` | lognormal median |
| `-latency-sigma` | `MOCK_LATENCY_SIGMA` | `0.5` | lognormal sigma (of ln ms) |
| `-rate-429` | `MOCK_RATE_429` | `0` | share answered 429 with `Retry-After` |
| `-rate-402` | `MOCK_RATE_402` | `0` | share answered 402 `in_flight_budget_exhausted` with `Retry-After` |
| `-rate-5xx` | `MOCK_RATE_5XX` | `0` | share answered with `-status-5xx` |
| `-rate-empty` | `MOCK_RATE_EMPTY` | `0` | share answered 200 with empty content |
| `-retry-after-s` | `MOCK_RETRY_AFTER_S` | `1` | `Retry-After` on 429/402 |
| `-status-5xx` | `MOCK_STATUS_5XX` | `503` | status of injected server errors |
| `-doc-types` | `MOCK_DOC_TYPES` | `durc` | classify mix, e.g. `durc=5,unilav=3,psc=1` |
| `-seed` | `MOCK_SEED` | `0` | random seed; 0 = clock |

The four rates are drawn once per request and must add up to at most 1.
Injected 429/402/5xx answer at once; successful and empty replies wait for the
sampled latency (a client that hangs up stops the wait). A bad value stops
the process at start-up.

## Metrics

| Metric | Labels |
|---|---|
| `mock_llm_requests_total` | `step`, `outcome` (`ok`, `empty`, `429`, `402`, `5xx`, `bad_request`, `cancelled`) |
| `mock_llm_request_duration_seconds` | `step`, `outcome` |
| `mock_llm_tokens_total` | `step`, `direction` (`prompt`, `completion`) |
| `mock_llm_in_flight_requests` | |

Plus the Go and process collectors.

## Run

```bash
cd mock-llm
go run . -latency lognormal -latency-median-ms 1500 -latency-sigma 0.6 -latency-max-ms 20000 \
  -rate-429 0.02 -doc-types durc=5,unilav=3,id_card=2

curl -s localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' -H 'Authorization: Bearer mock' \
  -d '{"model":"google/gemini-2.5-flash","messages":[
        {"role":"system","content":"You are an expert in classifying Italian administrative and legal documents."},
        {"role":"user","content":"Classify this document type:"}]}'
# {"choices":[{"finish_reason":"stop","index":0,"message":{"content":"durc","role":"assistant"}}],
#  "created":...,"id":"chatcmpl-mock-...","model":"google/gemini-2.5-flash","object":"chat.completion",
#  "usage":{"completion_tokens":1,"prompt_tokens":26,"total_tokens":27}}
```

Point the pipeline at it with an OpenAI-compatible provider whose base URL is
`http://<host>:8080/v1` and any API key.

## Image

```bash
docker buildx build --platform linux/arm64,linux/amd64 -t <repo>/mock-llm:<tag> --push .
```

Static binary on `distroless/static:nonroot`, port 8080.

## Tests

```bash
go test ./...
```

The tests run against `testdata/prompts.json`, the pipeline's real prompts
(every extract doc type, both legal checklists, rules, split, classify and
the follow-ups). After a prompt change in the pipeline, regenerate it and
re-run; then check the replies with the pipeline's own parsers (Pydantic
schemas, `build_giudizio`, `normalize_rules`, `parse_segments`, and the
rate-limit detection of 429/402) over HTTP:

```bash
cd <llm-backend>/pipeline
PYTHONPATH=. .venv/bin/python <this repo>/mock-llm/testdata/gen_prompts.py > <this repo>/mock-llm/testdata/prompts.json

(cd <this repo>/mock-llm && go run . -addr :18080) &
PYTHONPATH=. .venv/bin/python <this repo>/mock-llm/testdata/validate_replies.py http://localhost:18080/v1
```
