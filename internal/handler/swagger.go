package handler

import (
	_ "embed"
	"net/http"
	"strings"
)

//go:embed openapi.json
var openapiSpec []byte

// SwaggerUI serves the Swagger UI page at the configured path
// and the raw OpenAPI spec at {path}/openapi.json.
func SwaggerUI(basePath string) http.Handler {
	basePath = strings.TrimRight(basePath, "/")
	specPath := basePath + "/openapi.json"

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case specPath:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(openapiSpec)
		case basePath, basePath + "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(swaggerHTML(basePath)))
		default:
			http.NotFound(w, r)
		}
	})
}

func swaggerHTML(basePath string) string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>NoTalk API</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css">
  <style>html{box-sizing:border-box;overflow-y:scroll}*,*::before,*::after{box-sizing:inherit}body{margin:0;background:#fafafa}</style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    SwaggerUIBundle({
      url: "` + basePath + `/openapi.json",
      dom_id: "#swagger-ui",
      deepLinking: true,
      presets: [SwaggerUIBundle.presets.apis, SwaggerUIBundle.SwaggerUIStandalonePreset],
      layout: "BaseLayout"
    });
  </script>
</body>
</html>`
}
