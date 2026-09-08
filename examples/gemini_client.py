from google import genai
from google.genai import types
from pathlib import Path

client = genai.Client(
    api_key="your-api-key",
    http_options={
        "base_url": "http://localhost:4981/gemini",
        "api_version": "v1beta",
    },
)

# Model IDs are discovered from the signed-in Gemini Web account. Use one of
# the IDs returned here; gemini-advanced remains an alias for gemini-pro.
print("Available models:", [model.name for model in client.models.list()])

image_path = Path(__file__).with_name("fiber.png")

response = client.models.generate_content(
    model="gemini-advanced",
    contents=[
        types.Content(
            role="user",
            parts=[
                types.Part.from_text(text="Describe this image in detail."),
                types.Part.from_bytes(
                    data=image_path.read_bytes(),
                    mime_type="image/png",
                ),
            ],
        )
    ],
)

print(response.text)
