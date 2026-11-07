# LLM providers

Raggie's chat, answer scoring, memory extraction, skill generation and schedule
parsing all go through an **OpenAI-compatible `/v1/chat/completions`** endpoint.
There is no provider-specific SDK: if your model server speaks the OpenAI chat
API (streaming), Raggie can use it. Embeddings are separate and run locally by
default; see [embeddings.md](embeddings.md).

- [Single endpoint (simplest)](#single-endpoint-simplest)
- [Mock LLM (no key, offline)](#mock-llm-no-key-offline)
- [Ollama](#ollama)
- [vLLM and other self-hosted servers](#vllm-and-other-self-hosted-servers)
- [Several models: `LLM_MODEL_PROFILES_JSON`](#several-models-llm_model_profiles_json)
- [Choosing models per workspace](#choosing-models-per-workspace)
- [Reasoning, vision and tools](#reasoning-vision-and-tools)
- [Concurrency and timeouts](#concurrency-and-timeouts)

## Single endpoint (simplest)

```dotenv
LLMAAS_BASE_URL=https://api.openai.com/v1
LLMAAS_API_KEY=sk-...
LLMAAS_CHAT_MODEL=gpt-4o-mini
```

`LLMAAS_BASE_URL` is the base that ends in `/v1`; Raggie appends
`/chat/completions`. `LLMAAS_CHAT_MODEL` is the default model id sent upstream.
Any OpenAI-compatible service works the same way: OpenAI, Azure OpenAI behind a
compatible gateway, OpenRouter, Together, Groq, a corporate LLM gateway, or a
self-hosted server.

## Mock LLM (no key, offline)

`services/edge/cmd/mockllm` is a deterministic OpenAI-compatible server used for
demos and end-to-end tests. It answers from the sources in the prompt, citing up
to two of them as `[1]`, `[2]`, and returns canned output for the auxiliary calls
(memory extraction, schedule parsing).

```bash
make up-demo
```

starts it next to the `core` stack and points the edge, the eval and scheduler
workers and the control plane at it (`deploy/compose/docker-compose.demo.yml`),
whatever `.env` says. To use the mock with plain compose instead, start the
`mock` profile and set `LLMAAS_BASE_URL=http://mockllm:11435/v1` in `.env` (the
key is ignored unless `MOCKLLM_API_KEY` is set).
Knobs: `MOCKLLM_MODELS` (advertised model ids, default
`gpt-oss-120b,raggie-mock`), `MOCKLLM_TOKEN_DELAY_MS` (streaming pace),
`MOCKLLM_TOOL_CALLS`, `MOCKLLM_REASONING`, `MOCKLLM_API_KEY` (require a key),
`MOCKLLM_LANGUAGE` (answer template language, `en` or `tr`, default `en`; only
the template prose switches — citations keep their `[n]` labels either way).
The mock is for testing the pipeline; its answers are templated, not generated.

## Ollama

Raggie uses Ollama through Ollama's OpenAI-compatible endpoint (`/v1`); there is
no separate Ollama adapter.

**Bundled:** `make up-ollama` (or `install.sh --llm ollama`) adds an `ollama`
service to the compose stack and pulls `OLLAMA_MODEL` (default
`qwen3:4b-instruct`) on first start; `make up-ollama-gpu` adds NVIDIA GPU
passthrough. See [getting-started.md](getting-started.md).

**Your own Ollama** on the host or another machine: pull a model first, for
example `ollama pull qwen3:8b`. Simplest setup (one model):

```dotenv
LLMAAS_BASE_URL=http://host.docker.internal:11434/v1   # Docker Desktop; on Linux use the host IP
LLMAAS_API_KEY=ollama                                  # any non-empty value; Ollama ignores it
LLMAAS_CHAT_MODEL=qwen3:8b
```

On Linux, `host.docker.internal` is not defined by default; use the host's LAN
IP or add `extra_hosts: ["host.docker.internal:host-gateway"]` in a compose
override to the two services that call the LLM:

- `edge`: chat answers, web-research planning and the model list;
- `eval-worker`: the faithfulness judge (it uses `LLMAAS_BASE_URL`,
  `LLMAAS_API_KEY` and `LLMAAS_CHAT_MODEL`, also when
  `LLM_MODEL_PROFILES_JSON` is set).

The control plane makes no LLM calls, and the scheduler runs tasks through the
edge, so neither needs the host entry.

Practical notes:

- Pick a multilingual instruct model that handles Turkish well if your users
  write Turkish. The bundled default is `qwen3:4b-instruct` (fits CPU-only
  hosts); with a GPU, a larger model such as `qwen3:8b` answers better. It has
  not been benchmarked on the TR/EN evaluation set yet.
- On CPU, 7–8B models answer slowly; a GPU with ≥ 8 GB VRAM makes chat usable.
- Keep `max_concurrency` low (1–4) in a profile so a small Ollama box is not
  overwhelmed; Raggie queues the rest.

## vLLM and other self-hosted servers

vLLM, TGI, llama.cpp server, LM Studio and similar servers expose an
OpenAI-compatible API. Point `LLMAAS_BASE_URL` at it, set the served model name
in `LLMAAS_CHAT_MODEL`, and set `LLMAAS_API_KEY` to the key your server expects
(or any placeholder if it has none). For vLLM:

```bash
vllm serve Qwen/Qwen3-14B --api-key "$VLLM_KEY"   # serves http://<host>:8000/v1
```

```dotenv
LLMAAS_BASE_URL=http://vllm.internal:8000/v1
LLMAAS_API_KEY=your-vllm-key
LLMAAS_CHAT_MODEL=Qwen/Qwen3-14B
```

## Several models: `LLM_MODEL_PROFILES_JSON`

To route different models to different endpoints (say a local model for most
chats and a hosted vision model), set `LLM_MODEL_PROFILES_JSON` to a JSON array
on **one line**. When it is set, it replaces the single-endpoint mode and
unknown model ids are rejected.

| Field | Required | Meaning |
|---|---|---|
| `id` | yes | Canonical model id users and workspaces refer to (≤ 200 chars, unique). |
| `provider` | yes | A name for the endpoint. Profiles with the same provider share one concurrency pool and must use the same `base_url`, `api_key_env`, `max_concurrency` and `response_header_timeout_seconds`. |
| `base_url` | yes | OpenAI-compatible base URL (http/https, no credentials, query or fragment). |
| `api_key_env` | no | **Name** of the environment variable holding the key (never the key itself). If set but the variable is empty, the edge refuses to start. |
| `upstream_model` | no | Model name sent upstream (defaults to `id`). |
| `max_concurrency` | no | Concurrent upstream requests for this provider (1–4096). |
| `max_tokens` | no | Output token cap (1–1,000,000). |
| `response_header_timeout_seconds` | no | Seconds to wait for this provider's first response byte (5–900; defaults to `LLMAAS_RESPONSE_HEADER_TIMEOUT_SECONDS`, 60). Slow local models need more. Profiles with the same provider must agree. |
| `capabilities` | no | Any of `reasoning`, `tools`, `vision`. Requests needing a capability the model lacks are refused, or the option is stripped (reasoning effort). |

`LLMAAS_CHAT_MODEL` must be one of the `id`s and becomes the default. Example
(shown on several lines for readability; keep it on one line in `.env`):

```json
[
  {"id":"local-general","provider":"ollama","base_url":"http://ollama:11434/v1",
   "upstream_model":"qwen3:14b","max_concurrency":2,"max_tokens":4096,
   "capabilities":["reasoning","tools"]},
  {"id":"managed-vision","provider":"managed","base_url":"https://api.llm.example/v1",
   "api_key_env":"LLMAAS_API_KEY","upstream_model":"vision-model",
   "max_concurrency":32,"max_tokens":4096,"capabilities":["vision","tools"]}
]
```

Keys are referenced by variable name so the JSON itself contains no secrets. An
invalid registry stops the edge at boot with a message naming the problem.

## Choosing models per workspace

Platform admins manage the allowed model list and the default under *Admin → AI
models*, including friendly display names. Users can pick from the allowed list
in the chat composer; a skill can pin a model. Requests for a model outside the
allowed list are refused.

## Reasoning, vision and tools

- **Reasoning effort:** `LLMAAS_REASONING_EFFORT` (`low|medium|high`, default
  `medium`) is sent as `reasoning_effort`; users can override it per message.
  The model's reasoning stream is shown to the user only when
  `STREAM_REASONING=true` (off by default) and is never stored with the answer.
- **Vision:** image attachments (up to 4 per message, ~5 MB each) are sent only
  to models with the `vision` capability (profiles) or listed in `VISION_MODELS`
  (single-endpoint mode).
- **Tools:** web research and MCP tools need a tool-capable model (`tools`
  capability, or `TOOL_MODELS` in single-endpoint mode).

## Concurrency and timeouts

`LLMAAS_CHAT_MAX_CONCURRENCY` (default 64) bounds outbound requests per edge
replica and `EVAL_MAX_CONCURRENCY` (default 4; 1 in the bundled Ollama setup)
bounds judge calls per answer-scoring worker, so a busy chat cannot starve
scoring and vice versa. With profiles, each
provider has its own pool (`max_concurrency`). When the pool is full, the edge
sheds new chats with a retryable `busy` error instead of piling up requests.

Every option is listed in the [configuration reference](configuration.md).
