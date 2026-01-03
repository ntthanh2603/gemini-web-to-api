# AI Bridges 🚀

**AI Bridges** is a high-performance WebAI-to-API service built in Go. It allows you to convert web-based AI services (like Google Gemini) into standardized REST APIs, supporting **OpenAI**, **Anthropic (Claude)**, and **Google Native** protocols simultaneously.

[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat&logo=go)](https://golang.org/)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?style=flat&logo=docker)](https://github.com/ntthanh2603/ai-bridges/pkgs/container/ai-bridges)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](https://github.com/ntthanh2603/ai-bridges/blob/main/LICENSE)

---

## ✨ Features

- 🌉 **Universal AI Bridge**: Connects web-based AI models to your favorite apps.
- 🔌 **Multi-Protocol Support**: One server, three standards. Fully compatible with **OpenAI**, **Claude (Anthropic)**, and **Gemini Native** SDKs.
- 🔄 **Smart Session Management**: Automatically handles cookie rotation (`__Secure-1PSIDTS`) and persistence to keep connections alive.
- � **High Performance**: Built with Go and Fiber for efficiency and speed.
- � **Production Ready**: Includes Docker support, Swagger UI, and unified configuration.

---

## 🛠️ Technology Stack

- **Language**: Go (v1.24+)
- **Framework**: [Gofiber/fiber](https://github.com/gofiber/fiber)
- **HTTP Client**: [req/v3](https://github.com/imroc/req/v3)
- **Logging**: Uber-zap
- **Documentation**: Swag / Swagger

---

## 🚀 Getting Started

### Prerequisites

- Go 1.24 or higher installed. Or Docker.

### Configuration Priority

The application uses a unified configuration system with the following priority:

1. **Environment Variables** (Highest priority)
2. **`config.yml`**
3. **Defaults** (Lowest priority)

### Environment Variables

| Variable                  | Corresponding YAML Key    | Description                                             |
| ------------------------- | ------------------------- | ------------------------------------------------------- |
| `GEMINI_1PSID`            | `GEMINI_1PSID`            | (Required) Main session cookie                          |
| `GEMINI_1PSIDTS`          | `GEMINI_1PSIDTS`          | (Recommended) Timestamp cookie. Found in browser tools. |
| `GEMINI_1PSIDCC`          | `GEMINI_1PSIDCC`          | (Optional) Context cookie                               |
| `GEMINI_REFRESH_INTERVAL` | `GEMINI_REFRESH_INTERVAL` | Rotation interval in minutes (default: 30)              |
| `PORT`                    | `PORT`                    | Server port (default: 3000)                             |

### How to get Gemini Cookies

1. Go to [gemini.google.com](https://gemini.google.com)
2. Open DevTools (F12) -> **Application** tab -> **Cookies**
3. Find and copy values for `__Secure-1PSID` and `__Secure-1PSIDTS`

![Gemini Token Guide](assets/gemini_token_guide.png)

---

## 🐳 Docker Usage (Quick Start)

The easiest way to get started is to pull the pre-built image.

### 1. Pull the image

```bash
docker pull ghcr.io/ntthanh2603/ai-bridges:latest
```

### 2. Run container

```bash
docker run -d -p 3000:3000 \
  -e GEMINI_1PSID="your_psid_here" \
  -e GEMINI_1PSIDTS="your_psidts_here" \
  -e GEMINI_REFRESH_INTERVAL=30 \
  -v $(pwd)/cookies:/app/.cookies \
  --name ai-bridges \
  --restart unless-stopped \
  ghcr.io/ntthanh2603/ai-bridges:latest
```

---

## 🛠️ Building from Source

If you want to modify the code or run it locally without Docker.

### 1. Clone the repository

```bash
git clone https://github.com/ntthanh2603/ai-bridges.git
cd ai-bridges
```

### 2. Configure

Copy the example config and add your cookies:

```bash
cp config.example.yml config.yml
# Edit config.yml with your GEMINI_1PSID and GEMINI_1PSIDTS
```

### 3. Run the server

```bash
go run cmd/server/main.go
```

---

## 🧪 Quick Testing

Once the server is running, you can test the connection using any of the supported protocols.

### 1. OpenAI Compatible (Legacy/Universal)

Compatible with most AI clients (SDKs, LangChain, etc.).

```bash
curl -X POST http://localhost:3000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-pro",
    "messages": [{"role": "user", "content": "Hello, who are you?"}]
  }'
```

### 2. Claude Compatible (Python - Langchain)

Compatible with `langchain-anthropic` and standard Anthropic SDKs.

```python
from langchain_anthropic import ChatAnthropic

# Initialize the client pointing to our local bridge
llm = ChatAnthropic(
    base_url="http://localhost:3000",
    model="claude-3-5-sonnet-20240620",
    temperature=0.7,
    api_key="abc"
)
response = llm.invoke("Hello Claude! Please introduce yourself and explain how you can help me with coding.")
print(response.content)
```

For more examples (including streaming and other SDKs), check the [examples/](examples/) directory.

---

## 💡 Client Examples

You can find Python client examples in the `examples/` directory for widely used SDKs:

- **Claude/Anthropic**: [examples/claude_client.py](examples/claude_client.py)
- **OpenAI**: [examples/openai_client.py](examples/openai_client.py)
- **Gemini**: [examples/gemini_client.py](examples/gemini_client.py)

To run the Claude example:

```bash
cd examples
pip install langchain-anthropic
python claude_client.py
```

## 📘 API Documentation

Visit `http://localhost:3000/swagger/` for the full interactive API documentation.

![Swagger UI](assets/swagger.png)

---

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](https://github.com/ntthanh2603/ai-bridges/blob/main/LICENSE) file for details.
