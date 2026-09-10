package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
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

func CallOllamaOCRModel(base64ImageStr []string, modelID string, locale string) (*AnalysisResult, error) {
	if len(base64ImageStr) == 0 {
		return nil, fmt.Errorf("images=0 integration bug: no image payload attached to ollama request")
	}

	prompt := promptForModel(modelID, locale)
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

	timeout := ollamaHTTPRequestTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ollamaEndpoint, bytes.NewBuffer(jsonPayload))
	if err != nil {
		return nil, fmt.Errorf("failed to build ollama request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("ollama request timed out after %s", timeout)
		}
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

	parsed, err := parseOllamaJSONForLocale(ollamaResp.Response, modelID, locale)
	if err != nil {
		return nil, err
	}
	return parsed, nil
}

func parseOllamaJSON(raw string, modelID string) (*AnalysisResult, error) {
	return parseOllamaJSONForLocale(raw, modelID, "en-GB")
}

func parseOllamaJSONForLocale(raw string, modelID string, locale string) (*AnalysisResult, error) {
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
				result.Fields[fieldName] = normalizeFieldValueForLocale(fieldMap, locale)
			}
			return result, nil
		}

		parsed, err := parseInvoiceEnvelopeForLocale(envelope, locale)
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
	return parseInvoiceEnvelopeForLocale(envelope, "en-GB")
}

func parseInvoiceEnvelopeForLocale(envelope map[string]any, locale string) (*AnalysisResult, error) {
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
	result.Fields["InvoiceDate"] = makeDateFieldForLocale(stringPtrFromAny(envelope["invoiceDate"]), locale)
	result.Fields["DueDate"] = makeDateFieldForLocale(stringPtrFromAny(envelope["dueDate"]), locale)
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

	if rawTaxDetails, ok := envelope["taxDetails"].([]any); ok {
		rows := make([]map[string]FieldValue, 0, len(rawTaxDetails))
		for _, rawRow := range rawTaxDetails {
			rowObj, ok := rawRow.(map[string]any)
			if !ok {
				continue
			}
			amount := makeCurrencyField(numberPtrFromAny(rowObj["amount"]), currencyPtr)
			amount.Content = fmt.Sprintf("%.2f", valueOrAmountMoney(numberPtrFromAny(rowObj["amount"])))
			rate := makeStringField(stringPtrFromAny(rowObj["rate"]))
			rows = append(rows, map[string]FieldValue{
				"Amount": amount,
				"Rate":   rate,
			})
		}
		result.Fields["TaxDetails"] = FieldValue{Type: "array", Value: rows}
	}

	if result.Content != "" {
		inferInvoiceFieldsFromContent(result, result.Content)
	}
	return result, nil
}

func inferInvoiceFieldsFromContent(result *AnalysisResult, content string) {
	text := strings.TrimSpace(content)
	lines := splitLines(text)
	code := inferCurrencyCodeFromText(text)
	if code != "" && !hasFieldValue(result.Fields["CurrencyCode"]) {
		result.Fields["CurrencyCode"] = makeStringField(stringPtrFromAny(code))
	}

	if !hasFieldValue(result.Fields["InvoiceId"]) {
		if id := extractValueAfterLabel(lines, []string{"invoice no", "invoice #", "invoice number", "invoice id", "ref"}); id != "" {
			result.Fields["InvoiceId"] = makeStringField(stringPtrFromAny(id))
		}
	}
	if !hasFieldValue(result.Fields["InvoiceTotal"]) {
		if total := findCurrencyMatchOnLine(lines, []string{"gross amount", "amount due", "total due", "total"}); total != nil {
			result.Fields["InvoiceTotal"] = makeCurrencyField(total, stringPtrFromAny(code))
		}
	}
	if !hasFieldValue(result.Fields["SubTotal"]) {
		if subtotal := findCurrencyMatchOnLine(lines, []string{"net amount", "net total", "subtotal"}); subtotal != nil {
			result.Fields["SubTotal"] = makeCurrencyField(subtotal, stringPtrFromAny(code))
		}
	}
	if !hasFieldValue(result.Fields["TotalTax"]) {
		if tax := findCurrencyMatchOnLine(lines, []string{"vat", "tax", "vat amount", "tax amount"}); tax != nil {
			result.Fields["TotalTax"] = makeCurrencyField(tax, stringPtrFromAny(code))
		}
	}
	if !hasFieldValue(result.Fields["AmountDue"]) {
		if amountDue := findCurrencyMatchOnLine(lines, []string{"amount due"}); amountDue != nil {
			result.Fields["AmountDue"] = makeCurrencyField(amountDue, stringPtrFromAny(code))
		}
	}

	if !hasFieldValue(result.Fields["CustomerName"]) || !hasFieldValue(result.Fields["CustomerAddress"]) {
		if name, address := extractBillToFields(lines); name != "" || address != "" {
			if !hasFieldValue(result.Fields["CustomerName"]) && name != "" {
				result.Fields["CustomerName"] = makeStringField(stringPtrFromAny(name))
			}
			if !hasFieldValue(result.Fields["CustomerAddress"]) && address != "" {
				result.Fields["CustomerAddress"] = makeStringField(stringPtrFromAny(address))
			}
		}
	}

	if !hasFieldValue(result.Fields["VendorName"]) {
		if vendor := extractFirstStandaloneLine(lines); vendor != "" {
			result.Fields["VendorName"] = makeStringField(stringPtrFromAny(vendor))
		}
	}
}

func hasFieldValue(field FieldValue) bool {
	if field.Content != nil {
		return true
	}
	if field.Value == nil {
		return false
	}
	if m, ok := field.Value.(map[string]any); ok {
		for _, v := range m {
			if v != nil {
				return true
			}
		}
		return false
	}
	if s, ok := field.Value.(string); ok {
		return strings.TrimSpace(s) != ""
	}
	return true
}

func splitLines(text string) []string {
	return strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
}

func extractFirstStandaloneLine(lines []string) string {
	for _, line := range lines {
		clean := strings.TrimSpace(line)
		if clean == "" {
			continue
		}
		lower := strings.ToLower(clean)
		if strings.Contains(lower, "invoice") || strings.Contains(lower, "bill to:") || strings.Contains(lower, "due date") || strings.Contains(lower, "vat") || strings.Contains(lower, "amount") || strings.Contains(lower, "tax") {
			continue
		}
		return clean
	}
	return ""
}

func extractBillToFields(lines []string) (string, string) {
	for i, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		if !strings.Contains(lower, "bill to:") && !strings.Contains(lower, "bill to") {
			continue
		}
		name := ""
		start := i + 1
		if idx := strings.Index(line, ":"); idx >= 0 {
			candidate := strings.TrimSpace(line[idx+1:])
			if candidate != "" {
				name = candidate
			} else if start < len(lines) {
				candidate := strings.TrimSpace(lines[start])
				if candidate != "" && !isFieldBoundaryCandidate(candidate) {
					name = candidate
					start++
				}
			}
		}
		if name == "" {
			for j := start; j < len(lines); j++ {
				candidate := strings.TrimSpace(lines[j])
				if candidate == "" {
					continue
				}
				if isFieldBoundaryCandidate(candidate) {
					break
				}
				name = candidate
				start = j + 1
				break
			}
		}
		address := []string{}
		for j := start; j < len(lines); j++ {
			candidate := strings.TrimSpace(lines[j])
			if candidate == "" {
				continue
			}
			if isFieldBoundaryCandidate(candidate) || isLikelyItemDescriptionStart(lines, j) {
				break
			}
			address = append(address, candidate)
		}
		return name, strings.Join(address, "\n")
	}
	return "", ""
}

func isFieldBoundaryCandidate(line string) bool {
	lower := strings.ToLower(line)
	return strings.Contains(lower, "invoice date") || strings.Contains(lower, "due date") || strings.Contains(lower, "net amount") || strings.Contains(lower, "tax") || strings.Contains(lower, "gross amount") || strings.Contains(lower, "vat") || strings.Contains(lower, "total") || strings.Contains(lower, "amount due") || strings.Contains(lower, "invoice no") || strings.Contains(lower, "invoice #") || strings.Contains(lower, "invoice number")
}

func isLikelyItemDescriptionStart(lines []string, idx int) bool {
	if idx+1 >= len(lines) {
		return false
	}
	line := strings.TrimSpace(lines[idx])
	next := strings.TrimSpace(lines[idx+1])
	if line == "" || next == "" {
		return false
	}
	if strings.Contains(line, ",") || strings.Contains(line, ".") || strings.Contains(line, ":") ||
		strings.Contains(line, "£") || strings.Contains(line, "$") || strings.Contains(line, "€") || strings.Contains(line, "₹") {
		return false
	}
	if isQuantityLine(next) && len(strings.Fields(line)) >= 2 {
		return true
	}
	return false
}

func isQuantityLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	if _, err := strconv.Atoi(trimmed); err == nil {
		return true
	}
	return false
}

func inferCurrencyCodeFromText(text string) string {
	textLower := strings.ToLower(text)
	if strings.Contains(text, "€") || strings.Contains(textLower, "eur") {
		return "EUR"
	}
	if strings.Contains(text, "£") || strings.Contains(textLower, "gbp") {
		return "GBP"
	}
	if strings.Contains(text, "$") || strings.Contains(textLower, "usd") {
		return "USD"
	}
	if strings.Contains(text, "₹") || strings.Contains(textLower, "inr") {
		return "INR"
	}
	return ""
}

func extractValueAfterLabel(lines []string, patterns []string) string {
	for i, line := range lines {
		lower := strings.ToLower(line)
		for _, pattern := range patterns {
			patternIdx := strings.Index(lower, pattern)
			if patternIdx < 0 {
				continue
			}
			colonIdx := strings.Index(line, ":")
			if colonIdx >= 0 && colonIdx > patternIdx {
				candidate := strings.TrimSpace(line[colonIdx+1:])
				candidate = strings.Trim(candidate, ": \t\r\n")
				if candidate != "" && !isFieldBoundaryCandidate(candidate) && !isLikelyItemDescriptionStart(lines, i) {
					return strings.TrimSpace(candidate)
				}
			}
			if i+1 < len(lines) {
				candidate := strings.TrimSpace(lines[i+1])
				candidate = strings.Trim(candidate, ": \t\r\n")
				if candidate != "" && !isFieldBoundaryCandidate(candidate) && !isLikelyItemDescriptionStart(lines, i+1) {
					return strings.TrimSpace(candidate)
				}
			}
		}
	}
	return ""
}

func findCurrencyMatchOnLine(lines []string, patterns []string) *float64 {
	for i, line := range lines {
		lower := strings.ToLower(line)
		for _, pattern := range patterns {
			idx := strings.Index(lower, pattern)
			if idx < 0 {
				continue
			}
			candidate := ""
			colonIdx := strings.Index(line, ":")
			if colonIdx >= 0 && colonIdx > idx {
				candidate = strings.TrimSpace(line[colonIdx+1:])
			} else if i+1 < len(lines) {
				candidate = strings.TrimSpace(lines[i+1])
			}
			candidate = strings.Trim(candidate, ": \t\r\n")
			if curr := parseCurrencyAmount(candidate); curr != nil {
				return curr
			}
		}
	}
	return nil
}

func parseCurrencyAmount(text string) *float64 {
	text = strings.TrimSpace(strings.Trim(text, ": \t\r\n"))
	if text == "" {
		return nil
	}
	if regexp.MustCompile(`[A-Za-z]`).MatchString(text) {
		return nil
	}
	text = strings.NewReplacer(",", "", "€", "", "$", "", "£", "", "₹", "", " ", "", "\t", "", "\n", "", "\r", "").Replace(text)
	text = strings.TrimSpace(text)
	if text == "" || !regexp.MustCompile(`\d`).MatchString(text) {
		return nil
	}
	re := regexp.MustCompile(`[+-]?\d+(?:\.\d+)?`)
	match := re.FindString(text)
	if match == "" {
		return nil
	}
	f, err := strconv.ParseFloat(match, 64)
	if err != nil {
		return nil
	}
	return &f
}

func normalizeDateText(raw string) string {
	return normalizeDateTextForLocale(raw, "en-GB")
}

func normalizeDateTextForLocale(raw string, locale string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	trimmedLower := strings.ToLower(locale)
	if strings.Contains(trimmedLower, "en-gb") || strings.Contains(trimmedLower, "en-ie") || strings.Contains(trimmedLower, "en-au") || strings.Contains(trimmedLower, "en-nz") || strings.Contains(trimmedLower, "fr") || strings.Contains(trimmedLower, "de") || strings.Contains(trimmedLower, "es") || strings.Contains(trimmedLower, "it") {
		for _, layout := range []string{"02-01-2006", "02/01/2006", "02.01.2006", "01-02-2006", "01/02/2006", "01.02.2006", "2006-01-02", "2006/01/02"} {
			if t, err := time.Parse(layout, trimmed); err == nil {
				return t.Format("2006-01-02")
			}
		}
		return trimmed
	}
	for _, layout := range []string{"01-02-2006", "01/02/2006", "01.02.2006", "02-01-2006", "02/01/2006", "02.01.2006", "2006-01-02", "2006/01/02"} {
		if t, err := time.Parse(layout, trimmed); err == nil {
			return t.Format("2006-01-02")
		}
	}
	return trimmed
}

func normalizeFieldValue(field map[string]any) FieldValue {
	return normalizeFieldValueForLocale(field, "en-GB")
}

func normalizeFieldValueForLocale(field map[string]any, locale string) FieldValue {
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
	if strings.EqualFold(kind, "date") {
		candidate := ""
		if text, ok := fieldValue.Content.(string); ok {
			candidate = text
		}
		if candidate == "" {
			if text, ok := fieldValue.Value.(string); ok {
				candidate = text
			}
		}
		if candidate != "" {
			normalized := normalizeDateTextForLocale(candidate, locale)
			fieldValue.Content = normalized
			fieldValue.Value = normalized
		}
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
	return makeDateFieldForLocale(dateText, "en-GB")
}

func makeDateFieldForLocale(dateText *string, locale string) FieldValue {
	if dateText == nil {
		return FieldValue{Type: "date", Content: nil, Value: nil}
	}
	normalized := normalizeDateTextForLocale(*dateText, locale)
	return FieldValue{Type: "date", Content: normalized, Value: normalized}
}

func makeCurrencyField(value *float64, currency *string) FieldValue {
	if value == nil {
		return FieldValue{Type: "currency", Content: nil, Value: nil}
	}
	currencyCode := ""
	if currency != nil {
		currencyCode = *currency
	}
	return FieldValue{Type: "currency", Content: fmt.Sprintf("%.2f", *value), Value: map[string]any{"amount": *value, "currencyCode": currencyCode}}
}

func valueOrAmountMoney(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func promptForModel(modelID string, locale string) string {
	if strings.HasPrefix(modelID, "prebuilt-read") {
		return `Analyze the provided document and return readable content in a JSON object with "content" only.`
	}
	dateRule := "Dates in the source document may be written in DD-MM-YYYY or MM-DD-YYYY order. When normalizing to YYYY-MM-DD, treat the first number as the day and the second as the month for the requested locale."
	trimmed := strings.ToLower(strings.TrimSpace(locale))
	if !strings.Contains(trimmed, "en-gb") && !strings.Contains(trimmed, "en-ie") && !strings.Contains(trimmed, "en-au") && !strings.Contains(trimmed, "en-nz") && !strings.Contains(trimmed, "fr") && !strings.Contains(trimmed, "de") && !strings.Contains(trimmed, "es") && !strings.Contains(trimmed, "it") {
		dateRule = "Dates in the source document may be written in DD-MM-YYYY or MM-DD-YYYY order. When normalizing to YYYY-MM-DD, treat the first number as the month and the second as the day unless the text clearly uses a day-first convention."
	}
	return fmt.Sprintf(`You are an invoice data extraction engine.

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
- locale: %s
- %s
- invoiceDate/dueDate: return exactly as printed in the source document; do not convert or reformat the date string.
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

Return one complete JSON object and nothing else.`, locale, dateRule)
}

func ollamaHTTPRequestTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv("OLLAMA_HTTP_TIMEOUT_SECONDS")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 120 * time.Second
}

func modelName() string {
	if v := os.Getenv("OLLAMA_MODEL"); v != "" {
		return v
	}
	return "glm-ocr:bf16"
}
