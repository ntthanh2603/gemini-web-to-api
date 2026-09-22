# Video generation (experimental)

The bridge can submit a text-to-video request to Gemini Web, wait for the result,
and download the MP4. This requires an account that can generate videos in Gemini
Web, valid cookies, and available video quota. A model listed by `/openai/v1/models`
does not guarantee video access. This is an unofficial, experimental extension;
it does not provide OpenAI/Sora model compatibility.

## Create, poll, download

Windows CMD (keep the JSON escaping exactly as shown):

```cmd
curl http://127.0.0.1:4981/openai/v1/videos -H "Content-Type: application/json" -d "{\"model\":\"gemini-pro\",\"prompt\":\"A boy wearing a blue hat walking to school, animated illustration style.\"}"
```

The server returns HTTP 202 with an `id` and `status: "in_progress"`. Replace
`VIDEO_ID` below with that ID:

```cmd
curl http://127.0.0.1:4981/openai/v1/videos/VIDEO_ID
```

Poll approximately every 10 seconds. When `status` is `completed`:

```cmd
curl --fail http://127.0.0.1:4981/openai/v1/videos/VIDEO_ID/content -o "%USERPROFILE%\Desktop\gemini-video.mp4"
```

The content endpoint returns HTTP 409 until the job has completed successfully.
Failed jobs include a safe `error.code` and `error.message`.

## API contract and limits

- `POST /openai/v1/videos`: JSON `prompt` (required, 1–8000 bytes) and optional
  `model` (a Gemini model name or alias). Unknown fields are rejected. No duration,
  resolution, reference-image, audio, or multiple-video controls are supported.
- `GET /openai/v1/videos/{id}`: job status and local content URL when ready.
- `GET /openai/v1/videos/{id}/content`: authenticated upstream download is performed
  by the server; clients receive the cached MP4 without cookies or upstream URLs.
- One active video job per server process; additional creates return HTTP 429.
- Generation deadline: 10 minutes, including download. Download limit: 100 MiB.
- Up to 10 jobs retained; completed/failed jobs expire one hour after finishing.
  Job metadata is in memory and files are in a process-private temporary directory.
  Restarting loses job IDs. Normal shutdown deletes files; an abrupt process kill
  can leave `gemini-videos-*` directories in the OS temporary directory.
- Video requests use saved Gemini conversations even when `GEMINI_TEMPORARY=true`,
  because completion polling requires history. These conversations are not deleted.
- The bridge makes only one generation submission per job. It polls that existing
  conversation instead of resubmitting the prompt. On a timeout or connection
  failure, inspect Gemini Web before creating another job: the video may still
  be generating there. Repeating POST creates a new job; there is no idempotency key.
- The existing server has no per-user ownership checks for video jobs. Keep it
  local or place it behind authentication; job IDs are not an authentication layer.

## Troubleshooting

`no_video` means Gemini finished without returning a recognized video. Check the
same conversation in Gemini Web for account eligibility, usage limits or prompt
restrictions. It is not proof of an expired cookie.

`poll_failed` means status could not be read reliably. `invalid_response` means no
recognized video or conversation ID was received. Both may indicate a changed
Gemini Web protocol. `download_failed` can mean an expired media URL/session or
an unexpected response. `generation_timeout` means the local waiting deadline
expired; it does not cancel Google's generation.

## Implementation and verification

The provider recognizes only the structured generated-video card, including
positional and sparse JSPB layouts; arbitrary links are not treated as videos.
Downloads enforce HTTPS and trusted Google media hosts on every redirect, and
validate bounded MP4 container boxes and reject missing metadata/media data. Raw responses, cookies and signed URLs are not
included in job errors.

Protocol research reference: [Gemini-API](https://github.com/HanaokaYuzu/Gemini-API).
The Go implementation is independently written; no Python library dependency is
required. Synthetic fixtures exercise protocol variants and API lifecycle tests
use a fake provider. These tests do not establish live account compatibility.

### Polling failures and Gemini explanations

Jobs expose `conversation_id` and a bounded `provider_message` once the initial
reply arrives. URLs are removed from that message. Explicit limit/refusal replies
are reported immediately rather than polled as if a video were pending. Temporary
unrecognized status responses do not end a job after three attempts: read-only
polling continues within the original 10-minute deadline. Authentication failures
still stop promptly. A final `poll_failed` does not prove generation failed at
Google. Use the conversation reference to inspect Gemini Web before submitting
another prompt. Restarting the server still clears process-local jobs.

### Failure handling

The worker reserves its output file before sending the generation request. Failed
or interrupted downloads are removed, and a recovered worker panic becomes a safe
failed-job response. Concurrent creates admit only one active job. Shutdown cancels
active work and removes cached files; if the shutdown deadline expires, cleanup
continues once workers exit. Expired files that cannot yet be removed are retried
instead of losing their cleanup reference.

Container validation detects malformed boxes, truncated downloads, non-MP4 brands,
and size-limit violations. It does not decode the video or guarantee playback in
every player. Provider error messages are treated as explanations, not a promise
that the same prompt or account will succeed on a later attempt.

If an external temporary-file cleaner removes the idle video cache, the next job
allocates a fresh private directory before contacting Gemini. Permission, disk
and blocked-path errors still fail without consuming generation quota.
