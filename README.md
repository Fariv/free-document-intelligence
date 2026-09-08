# Free Document Intelligence

This repository is a small local Go HTTP server that behaves enough like the Azure Document Intelligence REST API for local development and integration testing.

It accepts document analysis requests, uploads a PDF or image, sends the image data to a local Ollama server, and stores the asynchronous operation result in memory. The server then returns an Azure-style polling response for the caller.

## What this service does

- Accepts Azure-style analyze routes:
  - `POST /documentintelligence/documentModels/prebuilt-invoice:analyze?api-version=2024-11-30`
  - `POST /documentintelligence/documentModels/prebuilt-read:analyze?api-version=2024-11-30`
- Accepts an async poll route:
  - `GET /documentintelligence/documentModels/{model}/analyzeResults/{resultId}?api-version=2024-11-30`
- Accepts an API key header:
  - `Ocp-Apim-Subscription-Key: local-test-key`
- Also accepts common local fallbacks:
  - `X-Api-Key`
  - `Api-Key`
  - `Authorization: Bearer <key>`
- Reads either a raw PDF/image body or a multipart upload file part.
- Returns an Azure-style `Operation-Location` header and a `Retry-After` header.
- Stores the operation in memory and exposes the result as a polling endpoint.

## Run locally

1. Copy the sample environment file:

```sh
cp .env.example .env
```

2. Edit the values if necessary.

Example values:

```env
OLLAMA_MODEL=glm-ocr:bf16
OLLAMA_BASE_URL=http://localhost:11434
DOCUMENT_INTELLIGENCE_API_KEY=local-test-key
OLLAMA_NUM_PREDICT=4096
PORT=8082
```

3. Start Ollama locally and make sure the model is available:

```sh
ollama pull glm-ocr:bf16
```

4. Build and run the Go binary:

```sh
go build -o bin/fdi
bin/fdi
```

The server listens on the configured `PORT`, or `8082` by default.

## Prerequisites

Before running the service locally, make sure the following are available:

- Go 1.22+ or the version that matches your workspace `go.mod`.
- A local Ollama service listening at `http://localhost:11434` or the value in `OLLAMA_BASE_URL`.
- The OCR model installed locally:

```sh
ollama pull glm-ocr:bf16
```

- A working PDF/image conversion dependency. The PDF path uses the `go-pdfium` WebAssembly integration, so a local Go runtime and the repository dependencies are required.
- Enough local RAM and CPU to run Ollama. The OCR model is a vision-capable model and can run best on a machine with a compatible GPU, though CPU execution may also work if your Ollama setup has the model available.
- A local environment file such as `.env` copied from `.env.example`.

If you do not have a GPU, CPU execution can still work for local testing, but it tends to be slower and may fail if the model memory or token settings exceed the hardware limitations of your environment.

## Request example

### Analyze invoice PDF

```sh
curl -X POST "http://localhost:8082/documentintelligence/documentModels/prebuilt-invoice:analyze?api-version=2024-11-30" \
  -H "Content-Type: application/pdf" \
  -H "Ocp-Apim-Subscription-Key: local-test-key" \
  --data-binary @invoice.pdf
```

The response is an Azure-style accepted response:

```json
{"status":"running"}
```

and includes an `Operation-Location` header that points to the polling route.

### Poll the result

```sh
curl "http://localhost:8082/documentintelligence/documentModels/prebuilt-invoice/analyzeResults/<operation-id>?api-version=2024-11-30"
```

A successful response is shaped like:

```json
{
  "status": "succeeded",
  "createdDateTime": "2026-09-08T00:00:00+00:00",
  "lastUpdatedDateTime": "2026-09-08T00:00:10+00:00",
  "analyzeResult": {
    "apiVersion": "2024-11-30",
    "modelId": "prebuilt-invoice",
    "content": "full extracted document text",
    "documents": [
      {
        "docType": "invoice",
        "fields": {
          "InvoiceId": {
            "type": "string",
            "content": "INV-1001",
            "valueString": "INV-1001"
          }
        }
      }
    ]
  }
}
```

If the server finishes with a model compatibility or parser failure, the public poll response will be downgraded to a safe error object:

```json
{
  "status": "failed",
  "error": {
    "code": "OllamaFailure",
    "message": "Document analysis failed."
  }
}
```

The raw model output is logged only server-side in development/debug mode and should not be returned to clients.

## Internal process

The workflow is intentionally simple:

1. A POST request is accepted on the invoice/read model route.
2. The uploaded file is written to the `uploads/invoices` directory.
3. The file is converted to an image payload if it is PDF or image input.
4. The image payload is posted to the configured Ollama endpoint.
5. The model returns a simplified internal JSON schema containing invoice fields and line item objects.
6. Go converts that simplified schema into the Azure-compatible field structure used by the response.
7. The uploaded file is removed after the async worker finishes.

## Notes for a PHP or service consumer

A PHP or other consumer application can point at this mock server exactly like a real Azure Document Intelligence endpoint:

- send the same `api-version` query string
- send the same key header or `Authorization: Bearer <key>` fallback
- POST the invoice file bytes
- read the returned `Operation-Location` URL
- poll the returned URL with `api-version=2024-11-30`

The implementation is intentionally close to Azure’s shape so the consumer code can be switched to the real Azure service later by changing only configuration values such as the endpoint base URL and API key.

## Repository files

- `main.go` contains the HTTP route registration and Azure-style response shape.
- `ollama_client.go` contains the Ollama request and simplified invoice parser.
- `pdf_processor.go` contains the PDF-to-image conversion helper.
- `main_test.go` contains route and parser regression tests.
