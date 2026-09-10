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

func TestParseOllamaJSONMapsTaxDetailsArrayAndObjectShape(t *testing.T) {
	raw := `{
		"documentType": "invoice",
		"content": "Invoice text",
		"invoiceId": "INV-001",
		"vendorName": "Acme",
		"vendorAddress": "",
		"customerName": "Contoso",
		"customerAddress": "",
		"invoiceDate": "2026-04-05",
		"dueDate": "",
		"purchaseOrder": "",
		"subtotal": 100.0,
		"totalTax": 20.0,
		"invoiceTotal": 120.0,
		"amountDue": 120.0,
		"currency": "GBP",
		"taxDetails": [
			{"amount": 200.0, "rate": "20%"},
			{"amount": 0.0, "rate": "0%"}
		]
	}`

	parsed, err := parseOllamaJSON(raw, "prebuilt-invoice")
	if err != nil {
		t.Fatalf("expected parser to accept invoice envelope with taxDetails array, got error: %v", err)
	}
	field, ok := parsed.Fields["TaxDetails"]
	if !ok {
		t.Fatal("expected parser to carry TaxDetails field map entry for the tax details array")
	}
	if field.Type != "array" {
		t.Fatalf("expected TaxDetails field type array, got %q", field.Type)
	}
	rows, ok := field.Value.([]map[string]FieldValue)
	if !ok {
		t.Fatalf("expected TaxDetails value to remain an array of object maps, got %T", field.Value)
	}
	if len(rows) != 2 {
		t.Fatalf("expected two tax detail rows, got %d", len(rows))
	}
	if _, ok := rows[0]["Amount"]; !ok {
		t.Fatal("expected TaxDetails row object to contain an Amount field")
	}
	if _, ok := rows[0]["Rate"]; !ok {
		t.Fatal("expected TaxDetails row object to contain a Rate field")
	}
}

func TestParseOllamaJSONInfersFieldsFromSynonymContentLabelsAndCurrencySymbol(t *testing.T) {
	raw := `{
		"documentType": "invoice",
		"content": "Rotterdam Bunkering & Logistics BV\nInvoice No.: RBL-2026-0774\nInvoice Date: 03-08-2026\nDue Date: 02-09-2026\nBill To: Clarksons Shipbroking Services Limited\nNet Amount: €3,200.00\nVAT (21%): €672.00\nGross Amount: €3,872.00",
		"invoiceId": "",
		"vendorName": "",
		"vendorAddress": "",
		"customerName": "",
		"customerAddress": "",
		"invoiceDate": "",
		"dueDate": "",
		"purchaseOrder": "",
		"subtotal": null,
		"totalTax": null,
		"invoiceTotal": null,
		"amountDue": null,
		"currency": ""
	}`

	parsed, err := parseOllamaJSON(raw, "prebuilt-invoice")
	if err != nil {
		t.Fatalf("expected parser to infer fields from synonym labels in content, got error: %v", err)
	}
	if got := parsed.Fields["InvoiceId"]; got.Type == "string" && got.Content == "" {
		t.Fatal("expected parser to recover InvoiceId from Invoice No. label")
	}
	if got := parsed.Fields["SubTotal"]; got.Type == "currency" && got.Content == "" {
		t.Fatal("expected parser to recover SubTotal from Net Amount label")
	}
	if got := parsed.Fields["TotalTax"]; got.Type == "currency" && got.Content == "" {
		t.Fatal("expected parser to recover TotalTax from VAT label")
	}
	if got := parsed.Fields["InvoiceTotal"]; got.Type == "currency" && got.Content == "" {
		t.Fatal("expected parser to recover InvoiceTotal from Gross Amount label")
	}
	if got := parsed.Fields["CurrencyCode"]; got.Type == "string" && got.Content == "" {
		t.Fatal("expected parser to infer CurrencyCode from the currency symbol in content")
	}
}

func TestParseOllamaJSONNormalizesDatesFromDDMMYYYYToISO(t *testing.T) {
	raw := `{
		"documentType": "invoice",
		"content": "Invoice text",
		"invoiceId": "INV-001",
		"vendorName": "Acme",
		"vendorAddress": "",
		"customerName": "Contoso",
		"customerAddress": "",
		"invoiceDate": "03-08-2026",
		"dueDate": "02-09-2026",
		"purchaseOrder": "",
		"subtotal": 100.0,
		"totalTax": 20.0,
		"invoiceTotal": 120.0,
		"amountDue": 120.0,
		"currency": "EUR"
	}`

	parsed, err := parseOllamaJSON(raw, "prebuilt-invoice")
	if err != nil {
		t.Fatalf("expected parser to accept invoice envelope and normalize date fields, got error: %v", err)
	}
	if got := parsed.Fields["InvoiceDate"].Content; got != "2026-08-03" {
		t.Fatalf("expected invoice date to be normalized as YYYY-MM-DD, got %v", got)
	}
	if got := parsed.Fields["DueDate"].Content; got != "2026-09-02" {
		t.Fatalf("expected due date to be normalized as YYYY-MM-DD, got %v", got)
	}
}

func TestParseOllamaJSONKeepsInvoiceAndAddressFallbacksLineBounded(t *testing.T) {
	raw := `{
		"documentType": "invoice",
		"content": "Rotterdam Bunkering & Logistics BV\nInvoice No.: INV-10482\nInvoice Date: 06-08-2026\nDue Date: 05-09-2026\nBill To:\nClarksons Shipbroking Services Limited\nSt. Magnus House\n3 Lower Thames Street\nLondon EC3R 6HD\nUnited Kingdom\nCrew Change Arrangements\n1\n1,000.00\n1,000.00\nNet Amount: £1,000.00\nVAT (20%): £200.00\nGross Amount: £1,200.00",
		"invoiceId": "",
		"vendorName": "",
		"vendorAddress": "",
		"customerName": "",
		"customerAddress": "",
		"invoiceDate": "",
		"dueDate": "",
		"purchaseOrder": "",
		"subtotal": null,
		"totalTax": null,
		"invoiceTotal": null,
		"amountDue": null,
		"currency": ""
	}`

	parsed, err := parseOllamaJSON(raw, "prebuilt-invoice")
	if err != nil {
		t.Fatalf("expected parser to recover invoice metadata from content line-boundaries, got error: %v", err)
	}
	invoiceID := parsed.Fields["InvoiceId"]
	if got, ok := invoiceID.Value.(string); !ok || got != "INV-10482" {
		t.Fatalf("expected parser to return a bounded InvoiceId value, got %#v", invoiceID.Value)
	}
	addr := parsed.Fields["CustomerAddress"]
	if got, ok := addr.Value.(string); ok && strings.Contains(got, "Crew Change Arrangements") {
		t.Fatalf("expected parser to stop CustomerAddress before an item-description row, got %q", got)
	}
}

func TestParseOllamaJSONInfersInvoiceAmountFieldsFromContentWhenEnvelopeValuesAreNull(t *testing.T) {
	raw := `{
		"documentType": "invoice",
		"content": "Rotterdam Bunkering & Logistics BV\nInvoice No.: INV-10482\nInvoice Date: 06-08-2026\nDue Date: 05-09-2026\nBill To:\nClarksons Shipbroking Services Limited\nSt. Magnus House\n3 Lower Thames Street\nLondon EC3R 6HD\nUnited Kingdom\nCrew Change Arrangements\n1\n1,000.00\n1,000.00\nNet Amount: £1,000.00\nVAT (20%): £200.00\nGross Amount: £1,200.00",
		"invoiceId": null,
		"vendorName": null,
		"vendorAddress": null,
		"customerName": null,
		"customerAddress": null,
		"invoiceDate": null,
		"dueDate": null,
		"purchaseOrder": null,
		"subtotal": null,
		"totalTax": null,
		"invoiceTotal": null,
		"amountDue": null,
		"currency": null
	}`

	parsed, err := parseOllamaJSON(raw, "prebuilt-invoice")
	if err != nil {
		t.Fatalf("expected parser to recover amount fields from the content fallback envelope, got error: %v", err)
	}
	if got := parsed.Fields["SubTotal"].Content; got == "" || got == "<nil>" {
		t.Fatalf("expected parser to recover SubTotal from Net Amount label in content, got %#v", parsed.Fields["SubTotal"])
	}
	if got := parsed.Fields["TotalTax"].Content; got == "" || got == "<nil>" {
		t.Fatalf("expected parser to recover TotalTax from VAT label in content, got %#v", parsed.Fields["TotalTax"])
	}
	if got := parsed.Fields["InvoiceTotal"].Content; got == "" || got == "<nil>" {
		t.Fatalf("expected parser to recover InvoiceTotal from Gross Amount label in content, got %#v", parsed.Fields["InvoiceTotal"])
	}
	if got := parsed.Fields["CustomerAddress"].Value; got == nil {
		t.Fatal("expected parser to keep a non-empty CustomerAddress recovery from the bounded bill-to scan")
	} else if addr, ok := got.(string); !ok || strings.Contains(addr, "Crew Change Arrangements") {
		t.Fatalf("expected parser to stop CustomerAddress before item lines, got %#v", parsed.Fields["CustomerAddress"])
	}
}

func TestPromptForModelIncludesLocaleAwareDateInstruction(t *testing.T) {
	prompt := promptForModel("prebuilt-invoice", "en-GB")
	if !strings.Contains(prompt, "DD-MM-YYYY") || !strings.Contains(prompt, "day") || !strings.Contains(prompt, "locale") {
		t.Fatalf("expected invoice prompt to carry an explicit locale-aware, day-first date instruction, got %q", prompt)
	}
}

func TestParseCurrencyAmountRejectsInvoiceIdentifierLikeText(t *testing.T) {
	if got := parseCurrencyAmount("INV-10482"); got != nil {
		t.Fatalf("expected parser to reject letter-bearing invoice IDs as a currency amount, got %#v", got)
	}
}

func TestNormalizeDateTextUsesDayFirstOrdering(t *testing.T) {
	if got := normalizeDateText("03-08-2026"); got != "2026-08-03" {
		t.Fatalf("expected DD-MM-YYYY input to normalize to 2026-08-03, got %q", got)
	}
	if got := normalizeDateText("02-09-2026"); got != "2026-09-02" {
		t.Fatalf("expected DD-MM-YYYY input to normalize to 2026-09-02, got %q", got)
	}
}

func TestParseOllamaJSONForLocaleNormalizesDateFieldsFromFieldsObject(t *testing.T) {
	raw := `{
		"content": "Invoice text",
		"fields": {
			"InvoiceDate": {
				"type": "date",
				"content": "03-08-2026",
				"value": "03-08-2026"
			}
		}
	}`

	parsed, err := parseOllamaJSONForLocale(raw, "prebuilt-invoice", "en-GB")
	if err != nil {
		t.Fatalf("expected parser to accept locale-aware date value fields, got error: %v", err)
	}
	if got := parsed.Fields["InvoiceDate"].Content; got != "2026-08-03" {
		t.Fatalf("expected locale-aware fields parser to normalize InvoiceDate to ISO date, got %#v", got)
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
