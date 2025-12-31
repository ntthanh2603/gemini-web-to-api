# AI Bridges: WebAI-to-API Service for Go

This project aims to bridge Web interfaces of AI assistants (Gemini, ChatGPT, Claude) to a standard API using Go (Fiber). It is equivalent to a "reverse-engineered" API wrapper exposed as a web service.

## Architecture

The project is structured to be modular and extensible.

### Directory Structure

```
ai-bridges/
├── cmd/
│   └── server/
│       └── main.go           # Entry point
├── internal/
│   ├── server/               # Fiber server setup and routes
│   └── providers/            # AI Provider implementations
│       ├── gemini/           # Google Gemini Web API implementation
│       │   ├── client.go
│       │   ├── constants.go
│       │   └── types.go
│       └── chatgpt/          # (Future) ChatGPT implementation
├── pkg/
│   └── utils/                # Shared utilities (cookie handling, parsing)
└── go.mod
```

## Prerequisities

- Go 1.21+
- Google Account Cookies (`__Secure-1PSID`, `__Secure-1PSIDTS`)

## How to get Cookies

You need to extract cookies from your browser to authenticate with Google Gemini.

1.  Open **[gemini.google.com](https://gemini.google.com)** in your browser (Chrome/Edge/Brave recommended).
2.  Make sure you are logged in.
3.  Press `F12` to open **Developer Tools**.
4.  Go to the **Application** tab (or **Storage** in Firefox).
5.  In the left sidebar, expand **Cookies** and select `https://gemini.google.com`.
6.  Find the row where **Name** is `__Secure-1PSID`. Copy its **Value**.
7.  Find the row where **Name** is `__Secure-1PSIDTS`. Copy its **Value**.
    - _Note: Recent Google changes might make `__Secure-1PSIDTS` optional or rotated frequently. If you don't see it, try reloading the page._

## Implementation Guide

### Step 1: Initialization

Initialize the Go module and install dependencies.

```bash
go mod init ai-bridges
go get -u github.com/gofiber/fiber/v2
go get -u github.com/imroc/req/v3  # Powerful HTTP client for Go
```

### Step 2: Define Interfaces

We define a common interface for all AI providers to ensure consistency.

```go
// internal/providers/provider.go
package providers

type AIProvider interface {
    Init() error
    GenerateContent(prompt string) (string, error)
}
```

### Step 3: Implement Gemini Client

The Gemini client implementation involves reversing the Web API logic:

1.  **Authentication**: Use `__Secure-1PSID` and `__Secure-1PSIDTS` cookies.
2.  **Handshake**: Perform a GET request to `https://gemini.google.com/app` to extract the `SNlM0e` token (nonce) required for POST requests.
3.  **Communication**: Send POST requests to `StreamGenerate` endpoint with `f.req` payload containing nested JSON structures.

### Step 4: Fiber Server

Expose the functionality via REST endpoints.

```go
// POST /api/v1/gemini/chat
{
    "message": "Hello world",
    "cookies": { ... }
}
```

## Future Work

- [ ] Add ChatGPT Provider
- [ ] Add Claude Provider
- [ ] Implement streaming responses
