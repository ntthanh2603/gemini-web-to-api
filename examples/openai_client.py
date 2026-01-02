from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:3000/v1",
    api_key=""
)

try:
    response = client.chat.completions.create(
        model="gpt-3.5-turbo",
        messages=[
            {"role": "system", "content": "You are a helpful assistant."},
            {"role": "user", "content": "Hello, who are you?"}
        ]
    )

    print(response.choices[0].message.content)

except Exception as e:
    print(f"Error: {e}")