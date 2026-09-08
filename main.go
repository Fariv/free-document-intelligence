package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/panjf2000/ants/v2"
)

type AzureField struct {
	Type          string         `json:"type"`
	Content       string         `json:"content,omitempty"`
	ValueString   string         `json:"valueString,omitempty"`
	ValueDate     string         `json:"valueDate,omitempty"`
	ValueCurrency *CurrencyValue `json:"valueCurrency,omitempty"`
	ValueNumber   *float64       `json:"valueNumber,omitempty"`
}

type CurrencyValue struct {
	Amount       float64 `json:"amount,omitempty"`
	CurrencyCode string  `json:"currencyCode,omitempty"`
}

type AzureDocument struct {
	DocType string                `json:"docType"`
	Fields  map[string]AzureField `json:"fields"`
}

type AzureAnalyzeResult struct {
	Documents []AzureDocument `json:"documents,omitempty"`
	Content   string          `json:"content,omitempty"`
}

type AzureSyncResponse struct {
	ApiVersion          string             `json:"apiVersion"`
	Status              string             `json:"status"`
	CreatedDateTime     string             `json:"createdDateTime"`
	LastUpdatedDateTime string             `json:"lastUpdatedDateTime,omitempty"`
	AnalyzeResult       AzureAnalyzeResult `json:"analyzeResult,omitempty"`
}

func errorJSON(code, message string) map[string]interface{} {
	return map[string]interface{}{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	}
}

var (
	dbStore      TaskRepository
	workerPool   *ants.Pool
	opStore      = NewOperationStore()
	apiVersion   = "2024-11-30"
	apiKeyEnvKey = "DOCUMENT_INTELLIGENCE_API_KEY"
	serverPort   = 8082
	serverHost   = "http://localhost"
)

func serverBaseURL() string {
	return fmt.Sprintf("%s:%d", serverHost, serverPort)
}

func NewOperationStore() *OperationStore {
	return &OperationStore{Ops: map[string]*Operation{}}
}

type Operation struct {
	ID        string
	ModelID   string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
	Result    *AnalysisResult
	Error     string
}

type OperationStore struct {
	mu  sync.RWMutex
	Ops map[string]*Operation
}

func (s *OperationStore) Create(id, modelID string) *Operation {
	now := time.Now()
	op := &Operation{ID: id, ModelID: modelID, Status: "running", CreatedAt: now, UpdatedAt: now}
	s.mu.Lock()
	s.Ops[id] = op
	s.mu.Unlock()
	return op
}

func (s *OperationStore) Get(id string) (*Operation, bool) {
	s.mu.RLock()
	op, ok := s.Ops[id]
	s.mu.RUnlock()
	return op, ok
}

func (s *OperationStore) Update(id, status string, result *AnalysisResult, errMsg string) {
	s.mu.Lock()
	op, ok := s.Ops[id]
	if ok {
		op.Status = status
		op.UpdatedAt = time.Now()
		op.Result = result
		op.Error = errMsg
	}
	s.mu.Unlock()
}

type AnalysisResult struct {
	Content      string                `json:"content,omitempty"`
	DocumentType string                `json:"documentType,omitempty"`
	Fields       map[string]FieldValue `json:"fields,omitempty"`
}

type FieldValue struct {
	Type    string      `json:"type"`
	Content interface{} `json:"content,omitempty"`
	Value   interface{} `json:"value,omitempty"`
}

type InvoiceJob struct {
	FilePath    string
	OperationID string
	ModelID     string
	ContentType string
}

func main() {
	loadDotEnv()

	dbStore = NewMemoryStore()

	var err error
	workerPool, err = ants.NewPool(2)
	if err != nil {
		fmt.Printf("Failed to create ants pool: %v", err)
	}
	defer workerPool.Release()

	// Start cleanup worker for expired tasks (2 minute TTL for testing)
	dbStore.StartCleanupWorker(2 * time.Minute)

	http.HandleFunc("GET /health", handleHealthCheck)
	http.HandleFunc("POST /documentintelligence/documentModels/prebuilt-invoice:analyze", handleAnalyzePOST)
	http.HandleFunc("POST /documentintelligence/documentModels/prebuilt-read:analyze", handleAnalyzePOST)
	http.HandleFunc("GET /documentintelligence/documentModels/prebuilt-invoice/analyzeResults/{resultId}", handleGetAnalyzeResults)
	http.HandleFunc("GET /documentintelligence/documentModels/prebuilt-read/analyzeResults/{resultId}", handleGetAnalyzeResults)

	if envPort := os.Getenv("PORT"); envPort != "" {
		if parsedPort, err := strconv.Atoi(envPort); err == nil && parsedPort > 0 {
			serverPort = parsedPort
		}
	}
	fmt.Printf("Server starting at %s:%d\n", serverHost, serverPort)
	if serverErr := http.ListenAndServe(":"+fmt.Sprint(serverPort), nil); serverErr != nil {
		panic(serverErr)
	}
}

func loadDotEnv() {
	file, err := os.Open(".env")
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, value)
		}
	}
}

func handleHealthCheck(resp http.ResponseWriter, req *http.Request) {
	resp.Header().Set("Content-Type", "text/plain; charset=utf-8")

	resp.WriteHeader(http.StatusOK)

	io.WriteString(resp, "Server health ok")
}

func handleAnalyzePOST(resp http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(resp, http.StatusMethodNotAllowed, "MethodNotAllowed", "Method Not Allowed")
		return
	}

	modelID := modelIDFromURL(req.URL.Path)
	if modelID == "" {
		writeError(resp, http.StatusBadRequest, "InvalidModel", "Unsupported model ID")
		return
	}
	if modelID != "prebuilt-read" && modelID != "prebuilt-invoice" {
		writeError(resp, http.StatusBadRequest, "InvalidModel", "Unsupported model ID")
		return
	}

	providedKey := keyFromRequest(req)
	if providedKey == "" {
		writeError(resp, http.StatusUnauthorized, "InvalidAuthenticationToken", "Missing API key")
		return
	}
	if providedKey != validAPIKey() {
		writeError(resp, http.StatusUnauthorized, "InvalidAuthenticationToken", "Invalid API key")
		return
	}

	if req.URL.Query().Get("api-version") != apiVersion {
		writeError(resp, http.StatusBadRequest, "InvalidRequest", "Unsupported API version.")
		return
	}

	contentType, body, err := readUploadBody(req)
	if err != nil {
		writeError(resp, http.StatusBadRequest, "InvalidRequest", err.Error())
		return
	}
	if !strings.HasPrefix(strings.ToLower(contentType), "application/pdf") &&
		!strings.HasPrefix(strings.ToLower(contentType), "image/jpeg") &&
		!strings.HasPrefix(strings.ToLower(contentType), "image/png") {
		writeError(resp, http.StatusUnsupportedMediaType, "InvalidContentType", "Unsupported content type")
		return
	}
	if len(body) == 0 {
		writeError(resp, http.StatusBadRequest, "InvalidRequest", "Empty document")
		return
	}

	const maxFileSize = 20 << 20
	if len(body) > maxFileSize {
		writeError(resp, http.StatusInsufficientStorage, "InvalidRequest", "Maximum file size 20 MB exceeded")
		return
	}

	uploadDir := "uploads/invoices"
	if err := os.MkdirAll(uploadDir, 0775); err != nil {
		writeError(resp, http.StatusInternalServerError, "InternalServerError", "Failed to prepare upload directory")
		return
	}
	fileext := ".pdf"
	if strings.HasPrefix(strings.ToLower(contentType), "image/jpeg") {
		fileext = ".jpg"
	}
	if strings.HasPrefix(strings.ToLower(contentType), "image/png") {
		fileext = ".png"
	}

	opID := uuid.New().String()
	finalfilename := fmt.Sprintf("upload-%s%s", opID, fileext)
	finalFilepath := filepath.Join(uploadDir, finalfilename)
	destFile, err := os.Create(finalFilepath)
	if err != nil {
		writeError(resp, http.StatusInternalServerError, "InternalServerError", "Failed to create working file")
		return
	}
	if _, err := destFile.Write(body); err != nil {
		_ = destFile.Close()
		writeError(resp, http.StatusInternalServerError, "InternalServerError", "Failed to save working file")
		return
	}
	_ = destFile.Close()

	if workerPool == nil {
		workerPool, err = ants.NewPool(2)
		if err != nil {
			_ = os.Remove(finalFilepath)
			opStore.Update(opID, "failed", nil, "Ollama processing failed")
			writeError(resp, http.StatusInternalServerError, "InternalServerError", "Failed to create worker pool")
			return
		}
	}

	opStore.Create(opID, modelID)
	job := InvoiceJob{FilePath: finalFilepath, OperationID: opID, ModelID: modelID, ContentType: contentType}
	if err := workerPool.Submit(func() { processInvoiceAsync(job) }); err != nil {
		_ = os.Remove(finalFilepath)
		opStore.Update(opID, "failed", nil, "Ollama processing failed")
		writeError(resp, http.StatusInternalServerError, "InternalServerError", "Failed to submit job to worker pool")
		return
	}

	resp.Header().Set("Content-Type", "application/json")
	resp.Header().Set("Operation-Location", fmt.Sprintf("%s/documentintelligence/documentModels/%s/analyzeResults/%s?api-version=%s", serverBaseURL(), modelID, opID, apiVersion))
	resp.Header().Set("Retry-After", "1")
	resp.WriteHeader(http.StatusAccepted)
	json.NewEncoder(resp).Encode(map[string]string{"status": "running"})
}

// processInvoiceAsync processes the uploaded document asynchronously.
func processInvoiceAsync(job InvoiceJob) {
	opID := job.OperationID
	filePath := job.FilePath
	defer func() {
		if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
			fmt.Printf("[%s] Failed to remove uploaded file %s: %v\n", opID, filePath, err)
		}
	}()

	fmt.Printf("[%s] Starting async processing...\n", opID)

	var base64Imgs []string
	var err error
	if strings.HasPrefix(strings.ToLower(job.ContentType), "application/pdf") {
		var pagenum int = 0
		outputpath := ""
		isFile := false
		base64Imgs, err = ConvertPdfToBase64Image(filePath, pagenum, &outputpath, &isFile)
		if err != nil {
			fmt.Printf("[%s] PDF conversion failed: %v\n", opID, err)
			opStore.Update(opID, "failed", nil, err.Error())
			return
		}
	} else {
		base64Imgs, err = encodeImageToBase64Slices(filePath)
		if err != nil {
			fmt.Printf("[%s] Image conversion failed: %v\n", opID, err)
			opStore.Update(opID, "failed", nil, err.Error())
			return
		}
	}

	extracted, err := CallOllamaOCRModel(base64Imgs, job.ModelID)
	if err != nil {
		fmt.Printf("[%s] Ollama model processing failed: %v\n", opID, err)
		opStore.Update(opID, "failed", nil, err.Error())
		return
	}

	fmt.Printf("[%s] Extraction succeeded!\n", opID)
	opStore.Update(opID, "succeeded", extracted, "")
}

// handleGetAnalyzeResults polls the operation state from the in-memory map.
func handleGetAnalyzeResults(resp http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		writeError(resp, http.StatusMethodNotAllowed, "MethodNotAllowed", "Method Not Allowed")
		return
	}
	if req.URL.Query().Get("api-version") != apiVersion {
		writeError(resp, http.StatusBadRequest, "InvalidRequest", "Unsupported API version.")
		return
	}

	modelID := modelIDFromURL(req.URL.Path)
	if modelID == "" || (modelID != "prebuilt-read" && modelID != "prebuilt-invoice") {
		writeError(resp, http.StatusBadRequest, "InvalidModel", "Unsupported model ID")
		return
	}
	resultID := strings.TrimPrefix(req.URL.Path, fmt.Sprintf("/documentintelligence/documentModels/%s/analyzeResults/", modelID))
	if resultID == "" || resultID == req.URL.Path {
		writeError(resp, http.StatusBadRequest, "InvalidRequest", "Operation ID is required")
		return
	}

	op, ok := opStore.Get(resultID)
	if !ok {
		writeError(resp, http.StatusNotFound, "ResultNotFound", "Operation not found")
		return
	}

	resp.Header().Set("Content-Type", "application/json")
	resp.WriteHeader(http.StatusOK)
	json.NewEncoder(resp).Encode(toAzurePollResponse(op))
}

func modelIDFromURL(path string) string {
	path = strings.TrimPrefix(path, "/documentintelligence/documentModels/")
	if strings.HasPrefix(path, "prebuilt-invoice:analyze") || strings.HasPrefix(path, "prebuilt-invoice/analyzeResults/") {
		return "prebuilt-invoice"
	}
	if strings.HasPrefix(path, "prebuilt-read:analyze") || strings.HasPrefix(path, "prebuilt-read/analyzeResults/") {
		return "prebuilt-read"
	}
	return ""
}

func keyFromRequest(req *http.Request) string {
	key := req.Header.Get("Ocp-Apim-Subscription-Key")
	if key != "" {
		return key
	}
	key = req.Header.Get("X-Api-Key")
	if key != "" {
		return key
	}
	key = req.Header.Get("Api-Key")
	if key != "" {
		return key
	}
	auth := req.Header.Get("Authorization")
	if auth != "" {
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			return strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
		}
		return strings.TrimSpace(auth)
	}
	return req.URL.Query().Get("key")
}

func readUploadBody(req *http.Request) (string, []byte, error) {
	contentType := req.Header.Get("Content-Type")
	if strings.HasPrefix(strings.ToLower(contentType), "multipart/form-data") {
		mr, err := req.MultipartReader()
		if err != nil {
			return "", nil, fmt.Errorf("failed to decode multipart upload")
		}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", nil, fmt.Errorf("failed to read multipart file part")
			}
			if part.FormName() == "file" || part.FormName() == "document" || part.FormName() == "pdf" {
				fileContentType := part.Header.Get("Content-Type")
				body, err := io.ReadAll(part)
				if err != nil {
					return "", nil, fmt.Errorf("failed to read multipart file")
				}
				return fileContentType, body, nil
			}
		}
		return "", nil, fmt.Errorf("missing document file part")
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return "", nil, fmt.Errorf("failed to read document")
	}
	return contentType, body, nil
}

func writeError(resp http.ResponseWriter, status int, code, message string) {
	resp.Header().Set("Content-Type", "application/json")
	resp.WriteHeader(status)
	json.NewEncoder(resp).Encode(errorJSON(code, message))
}

func validAPIKey() string {
	if v := os.Getenv("DOCUMENT_INTELLIGENCE_API_KEY"); v != "" {
		return v
	}
	if v := os.Getenv("DOCUMENT_AI_KEY"); v != "" {
		return v
	}
	return "local-test-key"
}

func toAzurePollResponse(op *Operation) map[string]interface{} {
	base := map[string]interface{}{
		"status":              op.Status,
		"createdDateTime":     op.CreatedAt.Format(time.RFC3339),
		"lastUpdatedDateTime": op.UpdatedAt.Format(time.RFC3339),
	}
	if op.Status == "succeeded" && op.Result != nil {
		fields := map[string]AzureField{}
		for name, fv := range op.Result.Fields {
			fields[name] = mapFieldValueToAzureField(name, fv)
		}
		base["analyzeResult"] = map[string]interface{}{
			"apiVersion": apiVersion,
			"modelId":    op.ModelID,
			"content":    op.Result.Content,
			"documents": []map[string]interface{}{
				{
					"docType": op.Result.DocumentType,
					"fields":  fields,
				},
			},
		}
	}
	if op.Status == "failed" {
		base["error"] = map[string]string{"code": "OllamaFailure", "message": "Document analysis failed."}
	}
	return base
}

func mapFieldValueToAzureField(name string, fv FieldValue) AzureField {
	field := AzureField{Type: fv.Type}
	if fv.Type == "string" {
		if s, ok := fv.Content.(string); ok {
			field.Content = s
		}
		if s, ok := fv.Value.(string); ok {
			field.ValueString = s
		}
	}
	if fv.Type == "date" {
		if s, ok := fv.Content.(string); ok {
			field.Content = s
		}
		if s, ok := fv.Value.(string); ok {
			field.ValueDate = s
		}
	}
	if fv.Type == "currency" {
		if s, ok := fv.Content.(string); ok {
			field.Content = s
		}
		if obj, ok := fv.Value.(map[string]any); ok {
			amount, _ := obj["amount"].(float64)
			code, _ := obj["currencyCode"].(string)
			field.ValueCurrency = &CurrencyValue{Amount: amount, CurrencyCode: code}
		}
	}
	if fv.Type == "number" {
		if n, ok := fv.Content.(float64); ok {
			field.Content = fmt.Sprintf("%.2f", n)
		}
		if n, ok := fv.Value.(float64); ok {
			v := n
			field.ValueNumber = &v
		}
	}
	if fv.Type == "array" {
		if items, ok := fv.Value.([]map[string]FieldValue); ok {
			_ = items
		}
	}
	return field
}

func stripos(haystack, needle string) int {
	lowerHaystack := strings.ToLower(haystack)
	lowerNeedle := strings.ToLower(needle)

	return strings.Index(lowerHaystack, lowerNeedle)
}

func cleanfilename(filename string) string {
	nonAlphaRegex := regexp.MustCompile(`[^a-zA-Z0-9\s-]+`)
	filename = nonAlphaRegex.ReplaceAllString(filename, "")

	spaceHyphenRegex := regexp.MustCompile(`[\s-]+`)
	filename = spaceHyphenRegex.ReplaceAllString(filename, "-")

	filename = strings.Trim(filename, "-")

	return filename
}
