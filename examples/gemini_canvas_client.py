"""Generate a Gemini Canvas through the bridge with the official Google GenAI SDK.

Canvas mode is selected by the "-canvas" model suffix. Each canvas comes back
as an inline_data part (mime_type, display_name = file name, data = bytes).

    pip install google-genai
    python examples/gemini_canvas_client.py --prompt "Write a short essay about coffee"
"""

import argparse
import os
import sys
from pathlib import Path

from google import genai

BASE_URL = os.environ.get("BRIDGE_URL", "http://localhost:4981")

client = genai.Client(
    api_key="not-needed",
    http_options={"base_url": f"{BASE_URL}/gemini", "api_version": "v1beta"},
)


def main() -> int:
    parser = argparse.ArgumentParser(description="Google GenAI SDK canvas example")
    parser.add_argument("--prompt", default="Write a short essay about coffee")
    parser.add_argument("--model", default="gemini-pro-canvas")
    parser.add_argument("--output-dir", type=Path, default=Path(__file__).parent)
    args = parser.parse_args()

    response = client.models.generate_content(model=args.model, contents=args.prompt)

    saved = 0
    for part in response.candidates[0].content.parts:
        if part.text and not part.thought:
            print(part.text)
        elif part.inline_data:
            canvas = part.inline_data
            path = args.output_dir / f"generated_canvas_{canvas.display_name}"
            path.write_bytes(canvas.data)
            print(f"\nSaved canvas ({canvas.mime_type}) to {path}")
            saved += 1

    if not saved:
        print("\nGemini answered without a canvas.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
