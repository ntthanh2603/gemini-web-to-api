"""Generate a Gemini Canvas through the bridge with the official OpenAI SDK.

Canvas mode is selected by the "-canvas" model suffix. The reply text contains
the canvas inline, and message.canvases lists each file with its content.

    pip install openai
    python examples/openai_canvas_client.py --prompt "Create a simple todo list web app"
"""

import argparse
import os
import sys
from pathlib import Path

from openai import OpenAI

BASE_URL = os.environ.get("BRIDGE_URL", "http://localhost:4981")

client = OpenAI(base_url=f"{BASE_URL}/openai/v1", api_key="not-needed")


def main() -> int:
    parser = argparse.ArgumentParser(description="OpenAI SDK canvas example")
    parser.add_argument("--prompt", default="Create a simple todo list web app")
    parser.add_argument("--model", default="gemini-pro-canvas")
    parser.add_argument("--output-dir", type=Path, default=Path(__file__).parent)
    args = parser.parse_args()

    completion = client.chat.completions.create(
        model=args.model,
        messages=[{"role": "user", "content": args.prompt}],
    )
    message = completion.choices[0].message
    print(message.content)

    # "canvases" is a bridge extension; the SDK exposes it as an attribute.
    canvases = getattr(message, "canvases", None) or []
    if not canvases:
        print("\nGemini answered without a canvas.")
        return 0
    for canvas in canvases:
        path = args.output_dir / f"generated_canvas_{canvas['file_name']}"
        path.write_text(canvas["content"], encoding="utf-8")
        print(f"\nSaved {canvas['type']} canvas '{canvas['title']}' ({canvas['mime_type']}) to {path}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
