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
	DueDate         *string               `json:"dueDate"`
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
	if !strings.HasPrefix(clean, "{") {
		return nil, fmt.Errorf("failed to parse model's internal JSON response: %s", clean)
	}

	var envelope map[string]any
	if err := json.Unmarshal([]byte(clean), &envelope); err == nil {
		if fieldsRaw, ok := envelope["fields"].(map[string]any); ok {
			result := &AnalysisResult{Content: "", DocumentType: "invoice", Fields: map[string]FieldValue{}}
			if content, ok := envelope["content"].(string); ok {
				result.Content = content
			}
			if docType, ok := envelope["documentType"].(string); ok && docType != "" {
				result.DocumentType = docType
			}
			if docType, ok := envelope["docType"].(string); ok && docType != "" {
				result.DocumentType = docType
			}
			for fieldName, rawField := range fieldsRaw {
				fieldMap, ok := rawField.(map[string]any)
				if !ok {
					continue
				}
				result.Fields[fieldName] = normalizeFieldValue(fieldMap)
			}
			return result, nil
		}

		parsed, err := parseInvoiceEnvelope(envelope)
		if err == nil {
			return parsed, nil
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

func parseInvoiceEnvelope(envelope map[string]any) (*AnalysisResult, error) {
	if strings.Contains(fmt.Sprint(envelope["content"]), "full readable text from the document") {
		return nil, fmt.Errorf("model returned prompt placeholder instead of extracted content")
	}

	result := &AnalysisResult{Content: "", DocumentType: "invoice", Fields: map[string]FieldValue{}}
	if content, ok := envelope["content"].(string); ok {
		result.Content = content
	}
	if docType, ok := envelope["documentType"].(string); ok && docType != "" {
		result.DocumentType = docType
	}
	if docType, ok := envelope["docType"].(string); ok && docType != "" {
		result.DocumentType = docType
	}

	result.Fields["InvoiceId"] = makeStringField(stringPtrFromAny(envelope["invoiceId"]))
	result.Fields["VendorName"] = makeStringField(stringPtrFromAny(envelope["vendorName"]))
	result.Fields["VendorAddress"] = makeStringField(stringPtrFromAny(envelope["vendorAddress"]))
	result.Fields["CustomerName"] = makeStringField(stringPtrFromAny(envelope["customerName"]))
	result.Fields["CustomerAddress"] = makeStringField(stringPtrFromAny(envelope["customerAddress"]))
	result.Fields["InvoiceDate"] = makeDateField(stringPtrFromAny(envelope["invoiceDate"]))
	result.Fields["DueDate"] = makeDateField(stringPtrFromAny(envelope["dueDate"]))
	result.Fields["PurchaseOrder"] = makeStringField(stringPtrFromAny(envelope["purchaseOrder"]))

	valueSubtotal := numberPtrFromAny(envelope["subtotal"])
	valueTax := numberPtrFromAny(envelope["totalTax"])
	valueInvoiceTotal := numberPtrFromAny(envelope["invoiceTotal"])
	valueAmountDue := numberPtrFromAny(envelope["amountDue"])

	currencyCode := normalizeCurrencyCode(stringValueFromAny(envelope["currency"]))
	currencyPtr := stringPtrFromAny(currencyCode)
	result.Fields["SubTotal"] = makeCurrencyField(valueSubtotal, currencyPtr)
	result.Fields["TotalTax"] = makeCurrencyField(valueTax, currencyPtr)
	result.Fields["InvoiceTotal"] = makeCurrencyField(valueInvoiceTotal, currencyPtr)
	result.Fields["AmountDue"] = makeCurrencyField(valueAmountDue, currencyPtr)
	result.Fields["CurrencyCode"] = makeStringField(currencyPtr)

	items := make([]map[string]FieldValue, 0)
	if rawItems, ok := envelope["items"].([]any); ok {
		for _, rawItem := range rawItems {
			object, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
			items = append(items, map[string]FieldValue{
				"Description": makeStringField(stringPtrFromAny(object["description"])),
				"Quantity":    makeNumberField(numberPtrFromAny(object["quantity"])),
				"UnitPrice":   makeCurrencyField(numberPtrFromAny(object["unitPrice"]), currencyPtr),
				"ProductCode": makeStringField(stringPtrFromAny(object["productCode"])),
				"Tax":         makeCurrencyField(numberPtrFromAny(object["tax"]), currencyPtr),
				"Amount":      makeCurrencyField(numberPtrFromAny(object["amount"]), currencyPtr),
			})
		}
	}
	result.Fields["Items"] = FieldValue{Type: "array", Value: items}
	return result, nil
}

func normalizeFieldValue(field map[string]any) FieldValue {
	kind, _ := field["type"].(string)
	fieldValue := FieldValue{Type: kind}
	if rawContent, ok := field["content"]; ok && rawContent != nil {
		fieldValue.Content = rawContent
	}
	if rawValue, ok := field["value"]; ok && rawValue != nil {
		fieldValue.Value = rawValue
	}
	if rawString, ok := field["valueString"].(string); ok {
		fieldValue.Value = rawString
	}
	if rawDate, ok := field["valueDate"].(string); ok {
		fieldValue.Value = rawDate
	}
	if rawNumber, ok := field["valueNumber"].(float64); ok {
		fieldValue.Value = rawNumber
	}
	return fieldValue
}

func stringPtrFromAny(value any) *string {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return &v
	case float64:
		str := fmt.Sprintf("%.0f", v)
		return &str
	case bool:
		str := strconv.FormatBool(v)
		return &str
	default:
		if str, ok := value.(fmt.Stringer); ok {
			text := str.String()
			if strings.TrimSpace(text) == "" {
				return nil
			}
			return &text
		}
		return nil
	}
}

func numberPtrFromAny(value any) *float64 {
	switch v := value.(type) {
	case nil:
		return nil
	case float64:
		return &v
	case int:
		f := float64(v)
		return &f
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return nil
		}
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil
		}
		return &f
	default:
		return nil
	}
}

func stringValueFromAny(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return fmt.Sprintf("%.0f", v)
	default:
		return fmt.Sprint(v)
	}
}

func normalizeCurrencyCode(code string) string {
	text := strings.TrimSpace(code)
	if text == "" {
		return ""
	}
	upper := strings.ToUpper(text)
	switch upper {
	case "₹", "RS", "RUPPEE", "INR", "RS.", "₹INR":
		return "INR"
	case "$", "USD":
		return "USD"
	case "€", "EUR":
		return "EUR"
	case "£", "GBP":
		return "GBP"
	case "BDT":
		return "BDT"
	default:
		return upper
	}
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

func makeDateField(dateText *string) FieldValue {
	if dateText == nil {
		return FieldValue{Type: "date", Content: nil, Value: nil}
	}
	return FieldValue{Type: "date", Content: *dateText, Value: *dateText}
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
  "dueDate": null,
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
- subtotal, totalTax, invoiceTotal, amountDue: JSON numbers only.
- currency: ISO currency code such as USD, EUR, GBP, BDT, or null.
- items: extract actual product/service rows.
- Do not put subtotal/tax/total rows in items.
- Do not assume invoiceTotal equals amountDue unless the document clearly says so.

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
