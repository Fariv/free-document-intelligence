package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type OllamaRequest struct {
	Model  string   `json:"model"`
	Prompt string   `json:"prompt"`
	Images []string `json:"images"`
	Stream bool     `json:"stream"`
	Format string   `json:"format"`
}

type OllamaResponse struct {
	Model     string    `json:"model"`
	CreatedAt time.Time `json:"created_at"`
	Response  string    `json:"response"`
	Done      bool      `json:"done"`
}

func CallOllamaOCRModel(base64ImageStr string) (map[string]string, error) {
	prompt := getprompt()
	payload := OllamaRequest{
		Model:  "glm-ocr:bf16",
		Prompt: prompt,
		Images: []string{base64ImageStr},
		Stream: false,
		Format: "json",
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("Failed to convert OllamaResponse to json: %w", err)
	}

	ollamaEndpoint := "http://localhost:11434/api/generate"

	resp, err := http.Post(ollamaEndpoint, "application/json", bytes.NewBuffer(jsonPayload))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Ollama server: %w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama returned bad status code: %d", resp.StatusCode)
	}

	var ollamaResp OllamaResponse
	err = json.NewDecoder(resp.Body).Decode(&ollamaResp)
	if err != nil {
		return nil, fmt.Errorf("failed to decode ollama response: %w", err)
	}

	var extractedFields map[string]string
	err = json.Unmarshal([]byte(ollamaResp.Response), &extractedFields)
	if err != nil {
		return nil, fmt.Errorf("failed to parse model's internal JSON response: %w. Raw text: %s", err, ollamaResp.Response)
	}

	return extractedFields, nil
}

func getprompt() string {
	var prompt string
	prompt = `Analyze this invoice image very carefully. Identify the following fields and extract their exact data:
	1. SupplierName (The name of the vendor/company issuing the invoice)
	2. TotalAmount (The final gross amount due, including taxes)
	3. VAT (Value Added Tax amount, if explicitly mentioned, otherwise set as "0.00")
	4. Tax (Any other tax or total tax amount, if mentioned, otherwise set as "0.00")

	You must strictly return your output as a flat JSON object with these exact keys: "SupplierName", "TotalAmount", "VAT", "Tax". 
	Do not include any conversational text, explanations, or markdown fences like triple backticks.`

	return prompt
}
