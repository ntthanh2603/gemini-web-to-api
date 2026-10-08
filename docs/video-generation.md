# Video generation

The bridge drives Gemini Web's native **Videos** tool (the one on
`gemini.google.com/videos`) and exposes it as asynchronous jobs through two
SDK-compatible APIs:

| API                  | SDK                                                      | Example                                                                |
| -------------------- | -------------------------------------------------------- | ---------------------------------------------------------------------- |
| OpenAI Videos API    | `openai` — `client.videos.create_and_poll`               | [`examples/openai_video_client.py`](../examples/openai_video_client.py) |
| Gemini Veo API       | `google-genai` — `client.models.generate_videos`         | [`examples/gemini_video_client.py`](../examples/gemini_video_client.py) |

Both share one job queue. It needs a Gemini account with video access and
remaining daily video quota; each video consumes one unit. The Claude API has
no video generation, so there is no Claude endpoint.

## OpenAI SDK

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:4981/openai/v1", api_key="not-needed")
video = client.videos.create_and_poll(model="sora-2", prompt="A cat walking on a beach at sunset",
                                      size="1280x720", poll_interval_ms=10_000)
client.videos.download_content(video.id).write_to_file("cat.mp4")
```

`sora-*` and `veo-*` model names use the account's default Gemini model; any
name from `/openai/v1/models` also works.

| Method & path                          | Notes                                                                  |
| -------------------------------------- | ---------------------------------------------------------------------- |
| `POST /openai/v1/videos`               | JSON or multipart. `prompt` (required, ≤ 8000 bytes), `model`, `size` (`1280x720` / `720x1280`) or `aspect_ratio` (`16:9` / `9:16`). `seconds` is accepted and ignored. |
| `GET /openai/v1/videos/{id}`           | Job status. `progress` is an estimate while `in_progress`.             |
| `GET /openai/v1/videos/{id}/content`   | The MP4. `409` until completed. Only `variant=video` exists.            |
| `GET /openai/v1/videos`                | Retained jobs, newest first.                                            |
| `DELETE /openai/v1/videos/{id}`        | Discards a finished job.                                                |

Plain HTTP:

```bash
curl -X POST http://localhost:4981/openai/v1/videos -H "Content-Type: application/json" \
  -d '{"prompt": "A cat walking on a beach at sunset", "aspect_ratio": "16:9"}'
curl http://localhost:4981/openai/v1/videos/VIDEO_ID            # poll every ~10 s
curl -o cat.mp4 http://localhost:4981/openai/v1/videos/VIDEO_ID/content
```

A video object looks like:

```json
{
  "id": "video_f222c6b42d7840198109cb0420e66864",
  "object": "video",
  "model": "gemini-3.1-pro",
  "status": "completed",
  "progress": 100,
  "created_at": 1791426529,
  "completed_at": 1791426598,
  "expires_at": 1791430198,
  "prompt": "A cat walking on a beach at sunset",
  "seconds": "10",
  "size": "1280x720",
  "error": null,
  "conversation_id": "c_1a2cd71e2dac5a85",
  "content_url": "/openai/v1/videos/video_f222c6b42d7840198109cb0420e66864/content",
  "message": "Your video is ready!"
}
```

## Google GenAI SDK

```python
import time
from google import genai
from google.genai import types

client = genai.Client(api_key="not-needed",
                      http_options={"base_url": "http://localhost:4981/gemini", "api_version": "v1beta"})
operation = client.models.generate_videos(
    model="veo-3.0-generate-001",
    source=types.GenerateVideosSource(prompt="A cat walking on a beach at sunset"),
    config=types.GenerateVideosConfig(aspect_ratio="16:9"),
)
while not operation.done:
    time.sleep(10)
    operation = client.operations.get(operation)
video = operation.response.generated_videos[0].video
client.files.download(file=video)
video.save("cat.mp4")
```

| Method & path                                          | Notes                                                       |
| ------------------------------------------------------ | ----------------------------------------------------------- |
| `POST /gemini/v1beta/models/{model}:predictLongRunning` | `instances[0].prompt`, `parameters.aspectRatio`. Image/video inputs and `sampleCount > 1` are rejected; `durationSeconds`, `resolution`, `negativePrompt` are ignored. |
| `GET /gemini/v1beta/models/{model}/operations/{id}`    | Long-running operation; `response.generateVideoResponse.generatedSamples[0].video.uri` when done. |
| `GET /gemini/v1beta/files/{id}:download?alt=media`     | The MP4.                                                    |

## Errors

Request errors: `400` invalid request or unknown model, `429` while another
video job is running, `404` unknown or expired job.

A failed job carries a code (OpenAI `error.code`; Gemini `error.status`):

| OpenAI code       | Gemini status        | Meaning                                                         |
| ----------------- | -------------------- | --------------------------------------------------------------- |
| `quota_exceeded`  | `RESOURCE_EXHAUSTED` | Gemini's video limit is reached; wait for the reset.            |
| `refused`         | `INVALID_ARGUMENT`   | Gemini declined the prompt (see `message`).                     |
| `timeout`         | `DEADLINE_EXCEEDED`  | Not ready within 10 minutes; it may still appear in Gemini Web. |
| `download_failed` | `INTERNAL`           | The video was generated but could not be downloaded.            |
| `no_video`        | `INTERNAL`           | Gemini returned neither a video nor a conversation to poll.     |
| `upstream_error`  | `INTERNAL`           | The generation request itself failed.                           |

## How it works

1. One `StreamGenerate` request with the Videos tool enabled: `inner[49] = 11`,
   `inner[55] = [[16]]` (landscape) or `[[17]]` (portrait), and the aspect ratio in
   `inner[0][9][6] = [[null,null,null,1|2]]`. The request is never retried, so one
   job uses at most one quota unit. Video conversations are always saved
   (ignoring `GEMINI_TEMPORARY`) because the result is read back from history.
2. Gemini answers quickly with text only; the video renders in the background
   (about 1–2 minutes). The bridge polls the conversation with the `hNvQHb`
   batchexecute RPC every 10 seconds without resubmitting the prompt.
3. The finished reply carries a video card in `candidate[12]` (JSPB field 60) whose
   download URL is on `contribution.usercontent.google.com`. The bridge downloads
   it with the session cookies and `authuser=<GEMINI_AUTH_USER>`, and checks that
   the bytes are an MP4.

## Limits

- One video job at a time per server; jobs live in memory and are lost on restart.
- Finished jobs and their MP4 are kept for one hour (at most 20 jobs).
- Video is 1280x720 or 720x1280, about 10 seconds, with audio, as decided by Gemini.
- Text-to-video only: no image/video input, duration, resolution or multiple-video controls.
- The API has no authentication of its own; keep the server on localhost or put it behind your own auth.
