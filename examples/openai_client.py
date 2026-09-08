import base64

from openai import OpenAI
from pathlib import Path

client = OpenAI(
    base_url="http://localhost:4981/openai/v1",
    api_key="not-needed"
)

def image_generation_example():
    # Inspect client.models.list() for the account's selectable web models.
    # gemini-pro selects Pro; unavailable Pro returns an error.
    response = client.images.generate(
        model="gemini-pro",
        prompt="A cinematic cyberpunk rabbit wearing a yellow raincoat, neon city, high detail",
        n=1,
        size="1024x1024",
    )

    image = response.data[0]
    if image.url:
        print("Generated image URL:", image.url)
        return

    if image.b64_json:
        output_path = Path(__file__).with_name("generated_image.png")
        output_path.write_bytes(base64.b64decode(image.b64_json))
        print("Generated image saved to:", output_path)


if __name__ == "__main__":
    image_generation_example()
