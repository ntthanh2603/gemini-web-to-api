# Music generation

The bridge drives Gemini Web's **Create music** tool and returns the track
through each SDK's audio-output call:

| SDK          | Call                                                         | Track                                       | Example                                                            |
| ------------ | ------------------------------------------------------------ | ------------------------------------------- | ------------------------------------------------------------------ |
| OpenAI       | `client.audio.speech.create(model, voice, input)`            | response body (`write_to_file`)             | [`openai_music_client.py`](../examples/openai_music_client.py)      |
| Google GenAI | `client.models.generate_content(model="gemini-pro-music")`   | `inline_data` part (`mime_type`, `data`)     | [`gemini_music_client.py`](../examples/gemini_music_client.py)      |

The Claude API has no audio output, so there is no Claude endpoint. A track
takes 1–3 minutes and uses the account's music quota.

## OpenAI SDK (`POST /openai/v1/audio/speech`)

Gemini Web has no text-to-speech, so this endpoint creates music:

| Field          | Meaning                                                                   |
| -------------- | ------------------------------------------------------------------------- |
| `input`        | The music prompt (required).                                              |
| `voice`        | `instrumental` or `vocals`; any other voice lets Gemini decide.           |
| `model`        | Any Gemini model (`gemini-pro-music`, `gemini-pro`); `tts-*` uses the default. |
| `instructions` | Appended to the prompt as style guidance.                                 |
| `length`       | Extension: `short` or `standard`.                                         |
| `genre`        | Extension: `pop`, `hip-hop`, `rock`, `k-pop`, `latin`, `electronic`, `r&b`, `country`, `afrobeats`, `reggae`, `jazz`, `classical`, `folk`, `lo-fi`, `acoustic`, `cinematic`, `ambient`. |

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:4981/openai/v1", api_key="not-needed", timeout=600, max_retries=0)
audio = client.audio.speech.create(
    model="gemini-pro-music",
    voice="instrumental",
    input="A calm lo-fi beat for studying",
    extra_body={"length": "short", "genre": "lo-fi"},
)
audio.write_to_file("lofi.mp3")
```

The response is the audio file in Gemini's format (`Content-Type` tells which);
`response_format` cannot convert it. Use `max_retries=0`: a retry would create
another track.

## Google GenAI SDK

Append `-music` to any model. Length, vocals and genre follow the prompt.

```python
from google import genai
from google.genai import types

client = genai.Client(api_key="not-needed", http_options=types.HttpOptions(
    base_url="http://localhost:4981/gemini", api_version="v1beta", timeout=600_000))
response = client.models.generate_content(model="gemini-pro-music", contents="An upbeat pop song about coffee")
for part in response.candidates[0].content.parts:
    if part.inline_data:
        open("coffee.mp3", "wb").write(part.inline_data.data)
```

## Errors

| HTTP (OpenAI) | `code`           | Meaning                                                            |
| ------------- | ---------------- | ------------------------------------------------------------------ |
| 400           | —                | Invalid length / vocals / genre, empty prompt or unknown model.    |
| 422           | `refused`        | Gemini answered in text without running its music tool.            |
| 429           | `quota_exceeded` | Music limit reached.                                               |
| 502           | `upstream_error` | Gemini Web reported an error ("I seem to be encountering an error"). |
| 504           | `timeout`        | No track within 5 minutes; it may still appear in Gemini Web.      |

The error message includes Gemini's reply when there is one.

## How it works

The request is a `StreamGenerate` call with the music tool selected, captured
from gemini.google.com:

| Field              | Value                                                                   |
| ------------------ | ----------------------------------------------------------------------- |
| `inner[49]`        | `21` (music tool)                                                       |
| `inner[0][9][6]`   | `[null, null, [length, vocals]]`; length short `4` / standard `5`, vocals instrumental `1` / on `2` |
| `inner[55]`        | `[[chip IDs]]`: short 42, standard 43, instrumental 26, vocals 27, then the genre (pop 44, hip-hop 45, rock 46, electronic 47, r&b 48, country 49, jazz 50, lo-fi 51, classical 52, acoustic 53, folk 54, latin 55, afrobeats 56, reggae 57, k-pop 40, cinematic 36, ambient 68) |

The request is never retried. If the stream ends without a track, the bridge
polls the conversation (`hNvQHb`) until an audio card appears, then downloads
it with the session cookies and `authuser=<GEMINI_AUTH_USER>`.

**Status:** the request format is verified against the web client. The audio
card format in the reply could not be observed yet (Gemini Web did not return
a track for the test account), so the bridge recognizes any item carrying an
`audio/*` MIME type and a Google media URL. If tracks are not detected, please
open an issue with the conversation ID.
