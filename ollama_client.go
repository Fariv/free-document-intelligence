package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type OllamaRequest struct {
	Model     string         `json:"model"`
	Prompt    string         `json:"prompt"`
	Images    []string       `json:"images"`
	Stream    bool           `json:"stream"`
	Format    string         `json:"format"`
	KeepAlive int            `json:"keep_alive,omitempty"`
	Options   map[string]any `json:"options,omitempty"`
}

type InternalInvoiceItem struct {
	Description *string  `json:"description"`
	Quantity    *float64 `json:"quantity"`
	UnitPrice   *float64 `json:"unitPrice"`
	ProductCode *string  `json:"productCode"`
	Tax         *float64 `json:"tax"`
	Amount      *float64 `json:"amount"`
}

type InternalInvoice struct {
	DocumentType    string                `json:"documentType"`
	Content         string                `json:"content"`
	InvoiceID       *string               `json:"invoiceId"`
	VendorName      *string               `json:"vendorName"`
	VendorAddress   *string               `json:"vendorAddress"`
	CustomerName    *string               `json:"customerName"`
	CustomerAddress *string               `json:"customerAddress"`
	InvoiceDate     *string               `json:"invoiceDate"`
	InvoiceDateText *string               `json:"invoiceDateText"`
	DueDate         *string               `json:"dueDate"`
	DueDateText     *string               `json:"dueDateText"`
	PurchaseOrder   *string               `json:"purchaseOrder"`
	Subtotal        *float64              `json:"subtotal"`
	TotalTax        *float64              `json:"totalTax"`
	InvoiceTotal    *float64              `json:"invoiceTotal"`
	AmountDue       *float64              `json:"amountDue"`
	Currency        *string               `json:"currency"`
	Items           []InternalInvoiceItem `json:"items"`
}

type OllamaResponse struct {
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
	Response  string    `json:"response"`
	Done      bool      `json:"done"`
	Error     string    `json:"error,omitempty"`
}

func CallOllamaOCRModel(base64ImageStr []string, modelID string) (*AnalysisResult, error) {
	if len(base64ImageStr) == 0 {
		return nil, fmt.Errorf("images=0 integration bug: no image payload attached to ollama request")
	}

	prompt := promptForModel(modelID)
	payload := OllamaRequest{
		Model:     modelName(),
		Prompt:    prompt,
		Images:    base64ImageStr,
		Stream:    false,
		Format:    "json",
		KeepAlive: 0,
		Options:   map[string]any{},
	}
	if v := os.Getenv("OLLAMA_NUM_PREDICT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			payload.Options["num_predict"] = n
		}
	} else {
		payload.Options["num_predict"] = 4096
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to convert OllamaResponse to json: %w", err)
	}

	ollamaEndpoint := strings.TrimRight(os.Getenv("OLLAMA_BASE_URL"), "/")
	if ollamaEndpoint == "" {
		ollamaEndpoint = "http://localhost:11434"
	}
	ollamaEndpoint += "/api/generate"

	fmt.Printf("model=%s images=%d image_bytes=%d prompt_version=invoice-v2\n", modelName(), len(base64ImageStr), len(base64ImageStr[0]))

	resp, err := http.Post(ollamaEndpoint, "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Ollama server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama returned bad status code: %d", resp.StatusCode)
	}

	var ollamaResp OllamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&ollamaResp); err != nil {
		return nil, fmt.Errorf("failed to decode ollama response: %w", err)
	}
	if ollamaResp.Error != "" {
		return nil, fmt.Errorf("ollama returned error: %s", ollamaResp.Error)
	}

	parsed, err := parseOllamaJSON(ollamaResp.Response, modelID)
	if err != nil {
		return nil, err
	}
	return parsed, nil
}

func parseOllamaJSON(raw string, modelID string) (*AnalysisResult, error) {
	clean := strings.TrimSpace(raw)
	if clean == "" {
		return nil, fmt.Errorf("empty response from ollama")
	}
	if strings.Contains(clean, "full readable text from the document") {
		return nil, fmt.Errorf("model returned prompt placeholder instead of extracted content")
	}
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```JSON")
	clean = strings.TrimSuffix(clean, "```")
	clean = strings.TrimSpace(clean)
	if clean == "" {
		return nil, fmt.Errorf("empty response from ollama")
	}
	if strings.HasPrefix(clean, "{") {
		var invoice InternalInvoice
		if err := json.Unmarshal([]byte(clean), &invoice); err == nil {
			if invoice.DocumentType == "" {
				invoice.DocumentType = "invoice"
			}
			if strings.Contains(invoice.Content, "full readable text from the document") {
				return nil, fmt.Errorf("model returned prompt placeholder instead of extracted content")
			}
			result := &AnalysisResult{Content: invoice.Content, DocumentType: invoice.DocumentType, Fields: map[string]FieldValue{}}
			result.Fields["InvoiceId"] = makeStringField(invoice.InvoiceID)
			result.Fields["VendorName"] = makeStringField(invoice.VendorName)
			result.Fields["VendorAddress"] = makeStringField(invoice.VendorAddress)
			result.Fields["CustomerName"] = makeStringField(invoice.CustomerName)
			result.Fields["CustomerAddress"] = makeStringField(invoice.CustomerAddress)
			result.Fields["InvoiceDate"] = makeDateField(invoice.InvoiceDate, invoice.InvoiceDateText)
			result.Fields["DueDate"] = makeDateField(invoice.DueDate, invoice.DueDateText)
			result.Fields["PurchaseOrder"] = makeStringField(invoice.PurchaseOrder)
			result.Fields["SubTotal"] = makeCurrencyField(invoice.Subtotal, invoice.Currency)
			result.Fields["TotalTax"] = makeCurrencyField(invoice.TotalTax, invoice.Currency)
			result.Fields["InvoiceTotal"] = makeCurrencyField(invoice.InvoiceTotal, invoice.Currency)
			result.Fields["AmountDue"] = makeCurrencyField(invoice.AmountDue, invoice.Currency)
			result.Fields["Currency"] = makeStringField(invoice.Currency)
			items := make([]map[string]FieldValue, 0, len(invoice.Items))
			for _, item := range invoice.Items {
				items = append(items, map[string]FieldValue{
					"Description": makeStringField(item.Description),
					"Quantity":    makeNumberField(item.Quantity),
					"UnitPrice":   makeCurrencyField(item.UnitPrice, invoice.Currency),
					"ProductCode": makeStringField(item.ProductCode),
					"Tax":         makeCurrencyField(item.Tax, invoice.Currency),
					"Amount":      makeCurrencyField(item.Amount, invoice.Currency),
				})
			}
			result.Fields["Items"] = FieldValue{Type: "array", Value: items}
			return result, nil
		}
	}

	var legacyMap map[string]string
	if err := json.Unmarshal([]byte(clean), &legacyMap); err == nil {
		parsed := &AnalysisResult{DocumentType: "invoice", Content: "", Fields: map[string]FieldValue{}}
		for k, v := range legacyMap {
			parsed.Fields[k] = FieldValue{Type: "string", Content: v, Value: v}
		}
		return parsed, nil
	}

	return nil, fmt.Errorf("failed to parse model's internal JSON response: %s", clean)
}

func makeStringField(value *string) FieldValue {
	if value == nil {
		return FieldValue{Type: "string", Content: nil, Value: nil}
	}
	return FieldValue{Type: "string", Content: *value, Value: *value}
}

func makeNumberField(value *float64) FieldValue {
	if value == nil {
		return FieldValue{Type: "number", Content: nil, Value: nil}
	}
	return FieldValue{Type: "number", Content: *value, Value: *value}
}

func makeDateField(dateText *string, visibleText *string) FieldValue {
	if dateText == nil && visibleText == nil {
		return FieldValue{Type: "date", Content: nil, Value: nil}
	}
	content := ""
	if visibleText != nil {
		content = *visibleText
	}
	value := ""
	if dateText != nil {
		value = *dateText
	}
	return FieldValue{Type: "date", Content: content, Value: value}
}

func makeCurrencyField(value *float64, currency *string) FieldValue {
	if value == nil {
		return FieldValue{Type: "currency", Content: nil, Value: map[string]any{"amount": nil, "currencyCode": nil}}
	}
	currencyCode := ""
	if currency != nil {
		currencyCode = *currency
	}
	return FieldValue{Type: "currency", Content: fmt.Sprintf("%.2f", *value), Value: map[string]any{"amount": *value, "currencyCode": currencyCode}}
}

func promptForModel(modelID string) string {
	if strings.HasPrefix(modelID, "prebuilt-read") {
		return `Analyze the provided document and return readable content in a JSON object with "content" only.`
	}
	return `You are an invoice data extraction engine.

Analyze the provided document and return exactly one valid JSON object.

Return JSON only.
No Markdown.
No code fences.
No explanation.
Do not copy this prompt.
Do not invent information.
If a value is not visible or cannot be reliably determined, return null.

Return exactly this shape:

{
  "documentType": "invoice",
  "content": "",
  "invoiceId": null,
  "vendorName": null,
  "vendorAddress": null,
  "customerName": null,
  "customerAddress": null,
  "invoiceDate": null,
  "invoiceDateText": null,
  "dueDate": null,
  "dueDateText": null,
  "purchaseOrder": null,
  "subtotal": null,
  "totalTax": null,
  "invoiceTotal": null,
  "amountDue": null,
  "currency": null,
  "items": []
}

Rules:
- content: readable document text.
- invoiceId: invoice number, not PO/account/customer number.
- vendorName: issuer/seller.
- customerName: bill-to/customer.
- invoiceDate/dueDate: normalized YYYY-MM-DD when unambiguous.
- invoiceDateText/dueDateText: original visible date text.
- subtotal, totalTax, invoiceTotal, amountDue: JSON numbers only.
- currency: ISO currency code such as USD, EUR, GBP, BDT, or null.
- items: extract actual product/service rows.
- Do not put subtotal/tax/total rows in items.
- Do not assume invoiceTotal equals amountDue unless the document clearly says so.
- If a numeric date is ambiguous, normalized date must be null while the original text remains available.

Each item must have this shape:

{
  "description": null,
  "quantity": null,
  "unitPrice": null,
  "productCode": null,
  "tax": null,
  "amount": null
}

Return one complete JSON object and nothing else.`
}

func modelName() string {
	if v := os.Getenv("OLLAMA_MODEL"); v != "" {
		return v
	}
	return "glm-ocr:bf16"
}
