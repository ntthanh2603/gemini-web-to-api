"""Generate music through the bridge with the official Google GenAI SDK.

Music is selected by the "-music" model suffix. The track comes back as an
inline_data audio part, like the Gemini API's audio-output models.

    pip install google-genai
    python examples/gemini_music_client.py --prompt "An upbeat instrumental pop song about coffee"

Each run consumes one track from your Gemini account's music quota.
"""

import argparse
import mimetypes
import os
import sys
from pathlib import Path

from google import genai
from google.genai import types

BASE_URL = os.environ.get("BRIDGE_URL", "http://localhost:4981")

client = genai.Client(
    api_key="not-needed",
    http_options=types.HttpOptions(base_url=f"{BASE_URL}/gemini", api_version="v1beta", timeout=600_000),
)


def main() -> int:
    parser = argparse.ArgumentParser(description="Google GenAI SDK music example")
    parser.add_argument("--prompt", default="An upbeat instrumental pop song about coffee")
    parser.add_argument("--model", default="gemini-pro-music")
    parser.add_argument("--output-dir", type=Path, default=Path(__file__).parent)
    args = parser.parse_args()

    print("Generating music; Gemini usually needs 1-3 minutes...")
    response = client.models.generate_content(model=args.model, contents=args.prompt)

    for part in response.candidates[0].content.parts:
        if part.text:
            print(part.text)
        elif part.inline_data:
            track = part.inline_data
            extension = mimetypes.guess_extension(track.mime_type or "") or ".mp3"
            path = args.output_dir / f"generated_music_gemini{extension}"
            path.write_bytes(track.data)
            print(f"Saved {len(track.data):,} bytes ({track.mime_type}) to {path}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
