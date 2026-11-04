package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestOpenAPISpecUsesRequestOrigin(t *testing.T) {
	app := fiber.New()
	app.Get("/openapi.json", OpenAPISpec)

	for _, origin := range []string{
		"http://127.0.0.1:18463",
		"http://127.0.0.1:19463",
		"https://gemini.example.com",
	} {
		t.Run(origin, func(t *testing.T) {
			response, err := app.Test(httptest.NewRequest(http.MethodGet, origin+"/openapi.json", nil))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
			}

			var spec map[string]json.RawMessage
			if err := json.NewDecoder(response.Body).Decode(&spec); err != nil {
				t.Fatal(err)
			}
			// Swagger 2.0 resolves omitted host and schemes against the URL
			// serving the specification, preserving the external host and port.
			for _, key := range []string{"host", "schemes"} {
				if _, exists := spec[key]; exists {
					t.Errorf("%s must be omitted so API requests use the specification's origin", key)
				}
			}
			if _, exists := spec["paths"]; !exists {
				t.Fatal("specification is missing paths")
			}
		})
	}
}
