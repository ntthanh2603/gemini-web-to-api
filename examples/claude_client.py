from langchain_anthropic import ChatAnthropic

# Initialize the client pointing to our local bridge
llm = ChatAnthropic(
    base_url="http://localhost:3000/claude/v1", 
    model="claude-3-5-sonnet-20240620",
    temperature=0.7,
    api_key="abc"
)
response = llm.invoke("Hello Claude! Please introduce yourself and explain how you can help me with coding.")
print(response.content)
