"""Generate a video through the bridge with the official Google GenAI SDK.

    pip install google-genai
    python examples/gemini_video_client.py --prompt "A cat walking on a beach at sunset"
    python examples/gemini_video_client.py --prompt "A skateboarder in Hanoi" --aspect 9:16

Each run consumes one video from your Gemini account's daily video quota.
"""

import argparse
import os
import sys
import time
from pathlib import Path

from google import genai
from google.genai import types

BASE_URL = os.environ.get("BRIDGE_URL", "http://localhost:4981")

client = genai.Client(
    api_key="not-needed",
    http_options={"base_url": f"{BASE_URL}/gemini", "api_version": "v1beta"},
)


def main() -> int:
    parser = argparse.ArgumentParser(description="Google GenAI SDK video generation example")
    parser.add_argument("--prompt", default="A cat walking on a beach at sunset, cinematic")
    parser.add_argument("--aspect", choices=["16:9", "9:16"], default="16:9")
    parser.add_argument("--output", type=Path, default=Path(__file__).with_name("generated_video_gemini.mp4"))
    args = parser.parse_args()

    # Veo model names are routed to the account's default Gemini model; any
    # Gemini model name from client.models.list() also works.
    operation = client.models.generate_videos(
        model="veo-3.0-generate-001",
        source=types.GenerateVideosSource(prompt=args.prompt),
        config=types.GenerateVideosConfig(aspect_ratio=args.aspect),
    )
    print(f"Started {operation.name}; Gemini usually needs 1-3 minutes")

    started = time.time()
    while not operation.done:
        time.sleep(10)
        operation = client.operations.get(operation)
        print(f"  [{int(time.time() - started):>3}s] still generating...")

    if operation.error:
        print(f"Video failed: {operation.error}", file=sys.stderr)
        return 1

    generated = operation.response.generated_videos[0]
    client.files.download(file=generated.video)
    generated.video.save(str(args.output))
    print(f"Saved {args.output.stat().st_size:,} bytes to {args.output}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
