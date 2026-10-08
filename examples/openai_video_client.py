"""Generate a video through the bridge with the official OpenAI SDK (Videos API).

    pip install openai
    python examples/openai_video_client.py --prompt "A cat walking on a beach at sunset"
    python examples/openai_video_client.py --prompt "A skateboarder in Hanoi" --size 720x1280

Each run consumes one video from your Gemini account's daily video quota.
"""

import argparse
import os
import sys
import time
import warnings
from pathlib import Path

from openai import OpenAI

# The SDK warns that OpenAI's hosted Sora API is shutting down; this bridge
# only reuses the Videos API shape, so the warning does not apply here.
warnings.filterwarnings("ignore", message="The Sora API", category=DeprecationWarning)

BASE_URL = os.environ.get("BRIDGE_URL", "http://localhost:4981")

client = OpenAI(base_url=f"{BASE_URL}/openai/v1", api_key="not-needed")


def main() -> int:
    parser = argparse.ArgumentParser(description="OpenAI SDK video generation example")
    parser.add_argument("--prompt", default="A cat walking on a beach at sunset, cinematic")
    parser.add_argument("--size", choices=["1280x720", "720x1280"], default="1280x720")
    parser.add_argument("--output", type=Path, default=Path(__file__).with_name("generated_video_openai.mp4"))
    args = parser.parse_args()

    # "sora-2" (the SDK default) is routed to the account's default Gemini model;
    # any Gemini model name from client.models.list() also works.
    video = client.videos.create(model="sora-2", prompt=args.prompt, size=args.size)
    print(f"Started {video.id}; Gemini usually needs 1-3 minutes")

    # Same loop as client.videos.create_and_poll, but with progress output.
    started = time.time()
    while video.status in ("queued", "in_progress"):
        time.sleep(10)
        video = client.videos.retrieve(video.id)
        print(f"  [{int(time.time() - started):>3}s] {video.status} ~{video.progress}%")

    print(f"{video.id}: {video.status} ({video.size}, {video.seconds}s)")
    if video.status != "completed":
        print(f"Video failed: {video.error}", file=sys.stderr)
        return 1

    content = client.videos.download_content(video.id, variant="video")
    content.write_to_file(args.output)
    print(f"Saved {args.output.stat().st_size:,} bytes to {args.output}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
