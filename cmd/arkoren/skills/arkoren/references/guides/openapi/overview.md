---
title: OpenAPI Overview
nav: Overview
description: Describe your first HTTP endpoint and export its OpenAPI contract without starting a server.
order: 10
---

# OpenAPI Overview

## Introduction

API consumers need to know which requests your service accepts and which responses
it returns. Add documentation beside your [route registrations](../http-fundamentals/routing.md),
then export an OpenAPI document from that router. An **operation** describes one
HTTP method and path; it does not replace the handler or enforce its behavior.

Neither the JSON document nor the optional Swagger UI is registered automatically.
You choose their paths, middleware, and network exposure independently of the API.
For messages sent through brokers rather than HTTP requests, use
[AsyncAPI](../asyncapi/overview.md). It exports a separate messaging contract;
Swagger UI does not render AsyncAPI documents.

## Describing your first endpoint

Install the core module in your Go project:

```sh
go get arkoren.dev/core
```

The following program defines a JSON handler, describes its response, and prints
the exported document. Save it as `main.go` and run `go run .`:

```go
package main

import (
	"fmt"
	"log"
	"net/http"

	"arkoren.dev/core/http/response"
	"arkoren.dev/core/http/router"
	"arkoren.dev/foundation/validate"
)

type Greeting struct {
	Message string `json:"message"`
}

func greet(writer http.ResponseWriter, incoming *http.Request) error {
	return response.JSON(writer, http.StatusOK, Greeting{Message: "Hello"})
}

func main() {
	routes := router.New()
	routes.Get("/greeting", greet, func(operation *router.OperationBuilder) {
		operation.JSONResponse(http.StatusOK, validate.Infer[Greeting]())
	})

	document, err := routes.OpenAPI("Greeting API", "1.0.0")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(document))
}
```

The output is deterministic OpenAPI 3.2.1 JSON with a `GET /greeting` operation
and its `200` JSON response schema. No request, application startup, listener, or
UI is needed to export the bytes. `validate.Infer[Greeting]()` describes the
output type; it does not validate input or check responses at runtime.

The operation callback runs once during registration, not for each request.
Routes without operation metadata still work, but are omitted from OpenAPI.
The router captures schemas and publishes detached metadata; retaining a draft
does not let you change the published description. Multiple callbacks run in
order on the same draft, and nil callbacks panic.

## Publishing your first JSON route

If clients need to fetch the document, add an explicit registration to the same
`routes` in `main`, after registering your API endpoints:

```go
routes.Get("/openapi.json", routes.OpenAPIHandler("Greeting API", "1.0.0"))
```

Serve `routes` using the [HTTP server](../http-networking/server.md). A request to
`/openapi.json` returns the generated JSON. This registration has no operation
callback, so it is not included in its own document. Generation is lazy and
cached, including failures; finish registering routes before serving requests.
The [serving guide](serving.md) explains that cache, access control, and adding a
browser UI.

The remaining examples in this group are focused changes to existing setup.
They retain error results but omit routine error checks unless failure handling
is the lesson. Complete programs handle errors before using results.

## Keeping the contract accurate

Documentation does not decode requests, run validation, set statuses or headers,
or force handlers to return declared responses. Keep it aligned with your actual
[request decoding](../http-fundamentals/requests.md), [responses](../http-fundamentals/responses.md), and
[error mapping](../http-fundamentals/error-handling.md). Middleware and authentication do not
automatically become responses or security requirements.

Use [Describing Operations](operations.md) for bodies, parameters, problems, and
security declarations. Use [Documenting Built-in Handlers](built-in-handlers.md)
for application probes and for the boundaries around webhooks, downloads, OAuth,
and mounted protocols.

For every declaration and export method, see the
[`router` API reference](https://pkg.go.dev/arkoren.dev/core/http/router).
