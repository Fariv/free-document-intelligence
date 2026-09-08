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
	mux.HandleFunc("GET /documentintelligence/documentModels/prebuilt-invoice/analyzeResults/{resultId}", handleGetAnalyzeResults)
	mux.HandleFunc("GET /documentintelligence/documentModels/prebuilt-read/analyzeResults/{resultId}", handleGetAnalyzeResults)
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

func TestSimulateStatusOnAnalyzeSubmitReturnsAzureErrorAndRetryAfter(t *testing.T) {
	mux := newAnalyzeMux()
	req := httptest.NewRequest(http.MethodPost, "/documentintelligence/documentModels/prebuilt-invoice:analyze?api-version=2024-11-30", nil)
	req.Header.Set("Content-Type", "application/pdf")
	req.Header.Set("Ocp-Apim-Subscription-Key", "local-test-key")
	req.Header.Set("X-Simulate-Status", "429")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 when X-Simulate-Status is supplied, got %d", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("expected Retry-After: 2 for simulated 429, got %q", got)
	}
	if !strings.Contains(w.Body.String(), "\"code\":\"RequestRateTooLarge\"") {
		t.Fatalf("expected an Azure-shaped simulated 429 error envelope, got %s", strings.TrimSpace(w.Body.String()))
	}
}

func TestSimulateStatusOnAnalyzePollReturnsAzureErrorAndRetryAfter(t *testing.T) {
	mux := newAnalyzeMux()
	req := httptest.NewRequest(http.MethodGet, "/documentintelligence/documentModels/prebuilt-invoice/analyzeResults/not-a-real-op?api-version=2024-11-30", nil)
	req.Header.Set("X-Simulate-Status", "503")
	w := httptest.NewRecorder()

	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when X-Simulate-Status is supplied on a poll call, got %d", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "" {
		t.Fatalf("expected no Retry-After header for a simulated 503, got %q", got)
	}
	if !strings.Contains(w.Body.String(), "\"code\":\"ServiceUnavailable\"") {
		t.Fatalf("expected an Azure-shaped simulated 503 error envelope, got %s", strings.TrimSpace(w.Body.String()))
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

func TestParseOllamaJSONAcceptsSimplifiedInvoiceWithTaxStringAndCurrencySymbol(t *testing.T) {
	raw := `{
		"documentType": "invoice",
		"content": "HishabKhata.com\nFinancial Management\nINVOICE\nINV-1-177540478844281639\nApril 06, 2026\nSELL\nBILL TO\nTerry Miller\nSTATUS\nDraft\n# PRODUCT QTY UNIT PRICE AMOUNT\n1 ASUS TUF GAMING F16 1 115000.00 ₹115000.00\nSub Total ₹115000.00\nDiscount - ₹1000.00\nTotal ₹114000.00\nGenerated by HishabKhata — Thank you for your business",
		"invoiceId": "1-177540478844281639",
		"vendorName": "HishabKhata.com",
		"vendorAddress": "",
		"customerName": "Terry Miller",
		"customerAddress": "",
		"invoiceDate": "2026-04-05",
		"dueDate": "",
		"purchaseOrder": "",
		"subtotal": 115000.0,
		"totalTax": "",
		"invoiceTotal": 114000.0,
		"amountDue": 114000.0,
		"currency": "₹"
	}`

	parsed, err := parseOllamaJSON(raw, "prebuilt-invoice")
	if err != nil {
		t.Fatalf("expected parser to normalize numeric/string mixed invoice payload, got error: %v", err)
	}
	if parsed.DocumentType != "invoice" {
		t.Fatalf("expected parser to preserve invoice document type, got %q", parsed.DocumentType)
	}
	if parsed.Content == "" {
		t.Fatal("expected parser to preserve invoice document content")
	}
	if _, ok := parsed.Fields["CurrencyCode"]; !ok {
		t.Fatal("expected parser to carry a mapped currency code field even when model emitted a symbol")
	}
}
