# Canvas

Gemini Web's **Canvas** tool writes a whole code file (for example a single-page
web app) or a document in one reply. The official Claude, OpenAI and Gemini APIs
have no canvas parameter: Artifacts, ChatGPT Canvas and Gemini Canvas exist only
in the web apps. The bridge therefore uses each SDK's normal chat call:

- **Turn it on with the model name**: append `-canvas` to any model, e.g.
  `gemini-3.1-pro-canvas` or the alias `gemini-pro-canvas`. No extra parameters.
- **Read it as SDK data**: every canvas is returned as a file object you read
  through the SDK (table below), and it is also inlined in the reply text in
  place of Gemini's `immersive_entry_chip` link (code as a fenced block,
  documents as Markdown), so plain chat clients still see it.

| SDK          | Call                              | Canvas files                                                                 | Example                                                               |
| ------------ | --------------------------------- | ---------------------------------------------------------------------------- | --------------------------------------------------------------------- |
| OpenAI       | `client.chat.completions.create`  | `message.canvases` (stream: `delta.canvases` in the last chunk)              | [`openai_canvas_client.py`](../examples/openai_canvas_client.py)       |
| Google GenAI | `client.models.generate_content`  | parts with `inline_data` (`display_name`, `mime_type`, `data` bytes)          | [`gemini_canvas_client.py`](../examples/gemini_canvas_client.py)       |
| Anthropic    | `client.messages.create`          | `message.canvases` (stream: in the `message_start` message)                  | [`claude_canvas_client.py`](../examples/claude_canvas_client.py)       |

Streaming works too. Canvas uses ordinary chat quota, not the video quota.

## Canvas file

`canvases` items (OpenAI, Anthropic) have these fields; the Gemini
`inline_data` part carries the same file as `display_name` / `mime_type` / `data`.

```json
{
  "id": "c_bc7925445b31d243_index.html",
  "title": "Simple To-Do List",
  "file_name": "index.html",
  "type": "code",
  "language": "html",
  "mime_type": "text/html",
  "content": "<!DOCTYPE html>\n<html lang=\"en\">…"
}
```

`type` is `code` or `document`; `content` is the raw file (no Markdown fence).

## Examples

```bash
python examples/openai_canvas_client.py --prompt "Create a simple todo list web app"
python examples/gemini_canvas_client.py --prompt "Write a short essay about coffee"
python examples/claude_canvas_client.py --prompt "Create a Python script for the first 10 Fibonacci numbers"
```

Each prints the reply and saves the canvas as `examples/generated_canvas_<file name>`.

OpenAI SDK:

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:4981/openai/v1", api_key="not-needed")
message = client.chat.completions.create(
    model="gemini-pro-canvas",
    messages=[{"role": "user", "content": "Create a simple todo list web app"}],
).choices[0].message
for canvas in message.canvases:
    open(canvas["file_name"], "w", encoding="utf-8").write(canvas["content"])
```

Google GenAI SDK:

```python
from google import genai

client = genai.Client(api_key="not-needed",
                      http_options={"base_url": "http://localhost:4981/gemini", "api_version": "v1beta"})
response = client.models.generate_content(model="gemini-pro-canvas", contents="Write a short essay about coffee")
for part in response.candidates[0].content.parts:
    if part.inline_data:
        open(part.inline_data.display_name, "wb").write(part.inline_data.data)
```

Plain HTTP:

```bash
curl http://localhost:4981/openai/v1/chat/completions -H "Content-Type: application/json" \
  -d '{"model": "gemini-pro-canvas", "messages": [{"role": "user", "content": "Create a counter web app"}]}'
```

`GET /openai/v1/models/gemini-pro-canvas` resolves the canvas variant; the
response `model` keeps the `-canvas` suffix.

## How it works

The request is a normal `StreamGenerate` call with the Canvas tool selected
(`inner[49] = 2`, using the web client's 99-field payload). Gemini streams the
canvas in `candidate[30]`:

| Field  | Meaning                                                       |
| ------ | ------------------------------------------------------------- |
| `[0]`  | canvas ID, `c_<conversation>_<file name>`                     |
| `[2]`  | title                                                         |
| `[4]`  | content (code is fenced)                                      |
| `[9]`  | file name, e.g. `index.html`, `coffee_essay.md`               |
| `[10]` | kind: `1` document, `2` code                                  |
| `[14]` | details; language at `[14][1]` (code) or `[14][4]` (document) |

The final stream frames omit `candidate[30]`, so the last frame that carried it
is kept, and `http://googleusercontent.com/immersive_entry_chip/N` in the text
is replaced with canvas `N`.

## Limits

- Each request creates a new canvas; editing an earlier canvas in place is not
  supported. Send the previous content back in the messages to revise it.
- Gemini decides whether to write code or a document, and may answer without a
  canvas; the examples print a note in that case.
- `google-genai`'s `response.text` warns that the response has non-text parts;
  read `candidates[0].content.parts` as the example does.
