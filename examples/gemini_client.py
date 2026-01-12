from google import genai
from google.genai import types

client = genai.Client(
    api_key="your-api-key",
    http_options={
        "base_url": "http://localhost:3000/gemini",
        "api_version": "v1beta"
    }
)

response = client.models.generate_content(
    model="gemini-1.5-flash",
    contents="How to make a REST API call in Python?"
)

print(response.text)
