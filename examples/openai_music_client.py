"""Generate music through the bridge with the official OpenAI SDK.

Gemini Web has no text-to-speech, so the bridge's audio/speech endpoint
creates music: `input` is the music prompt, `voice` picks "instrumental" or
"vocals" (any other voice leaves it to the prompt), `instructions` adds style
guidance, and `length` / `genre` are optional bridge extensions.

    pip install openai
    python examples/openai_music_client.py --prompt "A calm lo-fi beat for studying" --voice instrumental --genre lo-fi

Each run consumes one track from your Gemini account's music quota.
"""

import argparse
import os
import sys
from pathlib import Path

from openai import OpenAI

BASE_URL = os.environ.get("BRIDGE_URL", "http://localhost:4981")

# Music takes 1-3 minutes; raise the SDK's request timeout accordingly.
client = OpenAI(base_url=f"{BASE_URL}/openai/v1", api_key="not-needed", timeout=600, max_retries=0)


def main() -> int:
    parser = argparse.ArgumentParser(description="OpenAI SDK music example")
    parser.add_argument("--prompt", default="A calm lo-fi beat for studying")
    parser.add_argument("--voice", default="instrumental", help="instrumental, vocals, or any other value to let Gemini decide")
    parser.add_argument("--length", choices=["short", "standard"], default="short")
    parser.add_argument("--genre", default=None, help="e.g. pop, rock, lo-fi, jazz, cinematic")
    parser.add_argument("--output", type=Path, default=Path(__file__).with_name("generated_music_openai.mp3"))
    args = parser.parse_args()

    extra = {"length": args.length}
    if args.genre:
        extra["genre"] = args.genre

    print("Generating music; Gemini usually needs 1-3 minutes...")
    audio = client.audio.speech.create(
        model="gemini-pro-music",
        voice=args.voice,
        input=args.prompt,
        extra_body=extra,
    )
    audio.write_to_file(args.output)
    print(f"Saved {args.output.stat().st_size:,} bytes ({audio.response.headers.get('content-type')}) to {args.output}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
