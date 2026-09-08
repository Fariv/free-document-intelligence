package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newAnalyzeMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /documentintelligence/documentModels/prebuilt-invoice:analyze", handleAnalyzePOST)
	mux.HandleFunc("POST /documentintelligence/documentModels/prebuilt-read:analyze", handleAnalyzePOST)
	return mux
}

func TestMissingKeyReturns401(t *testing.T) {
	mux := newAnalyzeMux()
	req := httptest.NewRequest(http.MethodPost, "/documentintelligence/documentModels/prebuilt-invoice:analyze?api-version=2024-11-30", nil)
	req.Header.Set("Content-Type", "application/pdf")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for missing API key, got %d", w.Code)
	}
}

func TestInvalidKeyReturns401(t *testing.T) {
	mux := newAnalyzeMux()
	req := httptest.NewRequest(http.MethodPost, "/documentintelligence/documentModels/prebuilt-invoice:analyze?api-version=2024-11-30", nil)
	req.Header.Set("Content-Type", "application/pdf")
	req.Header.Set("Ocp-Apim-Subscription-Key", "wrong-key")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid API key, got %d", w.Code)
	}
}

func TestUnsupportedVersionReturns400(t *testing.T) {
	mux := newAnalyzeMux()
	req := httptest.NewRequest(http.MethodPost, "/documentintelligence/documentModels/prebuilt-invoice:analyze?api-version=2024-11-29", nil)
	req.Header.Set("Content-Type", "application/pdf")
	req.Header.Set("Ocp-Apim-Subscription-Key", "local-test-key")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unsupported API version, got %d", w.Code)
	}
}

func TestAuthorizationBearerKeyPassesValidation(t *testing.T) {
	mux := newAnalyzeMux()
	req := httptest.NewRequest(http.MethodPost, "/documentintelligence/documentModels/prebuilt-invoice:analyze?api-version=2024-11-30", bytes.NewReader([]byte("%PDF-1.4 test")))
	req.Header.Set("Content-Type", "application/pdf")
	req.Header.Set("Authorization", "Bearer local-test-key")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 after Authorization bearer API key is accepted, got %d and body: %s", w.Code, strings.TrimSpace(w.Body.String()))
	}
}

func TestParseOllamaJSONAcceptsContentAndFieldsWithoutDocumentType(t *testing.T) {
	raw := `{
		"content": "Invoice text",
		"fields": {
			"InvoiceId": {
				"type": "string",
				"content": null,
				"value": "INV-001"
			}
		}
	}`

	parsed, err := parseOllamaJSON(raw, "prebuilt-invoice")
	if err != nil {
		t.Fatalf("expected parser to accept invoice envelope without explicit documentType, got error: %v", err)
	}
	if parsed.DocumentType != "invoice" {
		t.Fatalf("expected parser to default missing documentType to invoice, got %q", parsed.DocumentType)
	}
	if parsed.Content != "Invoice text" {
		t.Fatalf("expected parser to keep content field string, got %q", parsed.Content)
	}
	if _, ok := parsed.Fields["InvoiceId"]; !ok {
		t.Fatal("expected parser to keep field map entries when content is present")
	}
}
