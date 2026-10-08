"""Generate a Gemini Canvas through the bridge with the official Anthropic SDK.

Canvas mode is selected by the "-canvas" model suffix. The text block contains
the canvas inline, and message.canvases lists each file with its content.

    pip install anthropic
    python examples/claude_canvas_client.py --prompt "Create a Python script for the first 10 Fibonacci numbers"
"""

import argparse
import os
import sys
from pathlib import Path

import anthropic

BASE_URL = os.environ.get("BRIDGE_URL", "http://localhost:4981")

client = anthropic.Anthropic(base_url=f"{BASE_URL}/claude", api_key="not-needed")


def main() -> int:
    parser = argparse.ArgumentParser(description="Anthropic SDK canvas example")
    parser.add_argument("--prompt", default="Create a Python script for the first 10 Fibonacci numbers")
    parser.add_argument("--model", default="gemini-pro-canvas")
    parser.add_argument("--output-dir", type=Path, default=Path(__file__).parent)
    args = parser.parse_args()

    message = client.messages.create(
        model=args.model,
        max_tokens=8192,
        messages=[{"role": "user", "content": args.prompt}],
    )
    for block in message.content:
        if block.type == "text":
            print(block.text)

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
