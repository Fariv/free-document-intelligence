package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/panjf2000/ants/v2"
)

type AzureField struct {
	Type        string `json:"type"`
	ValueString string `json:"valueString"`
	Content     string `json:"content"`
}

type AzureDocument struct {
	DocType string                `json:"docType"`
	Fields  map[string]AzureField `json:"fields"`
}

type AnalyzeResult struct {
	Documents []AzureDocument `json:"documents"`
}

type AzureSyncResponse struct {
	ApiVersion      string        `json:"apiVersion"`
	Status          string        `json:"status"`
	CreatedDateTime string        `json:"createdDateTime"`
	AnalyzeResult   AnalyzeResult `json:"analyzeResult"`
}

var (
	dbStore    TaskRepository
	workerPool *ants.Pool
)

type InvoiceJob struct {
	FilePath    string
	OperationID string
}

func main() {
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
	http.HandleFunc("GET /documentintelligence/documentModels/prebuilt-invoice/analyzeResults/{opID}", handleGetAnalyzeResults)

	var port int = 8082
	fmt.Printf("Server starting at http://localhost:%d\n", port)
	if serverErr := http.ListenAndServe(":"+fmt.Sprint(port), nil); serverErr != nil {
		panic(serverErr)
	}
}

func handleHealthCheck(resp http.ResponseWriter, req *http.Request) {
	resp.Header().Set("Content-Type", "text/plain; charset=utf-8")

	resp.WriteHeader(http.StatusOK)

	io.WriteString(resp, "Server health ok")
}

func handleAnalyzePOST(resp http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(resp, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	file, fileHandler, err := req.FormFile("file")
	if err != nil {
		http.Error(resp, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	filename := fileHandler.Filename
	contentType := fileHandler.Header.Get("Content-Type")
	fileSize := fileHandler.Size

	const maxFileSize = 20 << 20 // 20mb

	if fileSize > maxFileSize {
		http.Error(resp, "Maximum file size 20 MB exceeded", http.StatusInsufficientStorage)
		return
	}

	if stripos(contentType, "pdf") < 0 {
		http.Error(resp, "Only pdf file is accepted", http.StatusUnprocessableEntity)
		return
	}

	// Print metadata to server logs
	fmt.Printf("--- New Upload Metadata ---\n")
	fmt.Printf("Original Filename: %s\n", filename)
	fmt.Printf("Size:              %d bytes\n", fileSize)
	fmt.Printf("MIME Content-Type: %s\n", contentType)

	var uploadDir string

	uploadDir = "uploads/invoices"

	err = os.MkdirAll(uploadDir, 0775)

	if err != nil {
		http.Error(resp, err.Error(), http.StatusInternalServerError)
		return
	}

	opID := uuid.New().String()

	fileext := filepath.Ext(filename)
	filename = strings.TrimSuffix(filename, fileext)

	filename = cleanfilename(filename)

	finalfilename := filename + "-" + opID + fileext

	finalFilepath := fmt.Sprintf("%s/%s", uploadDir, finalfilename)

	destFile, err := os.Create(finalFilepath)
	if err != nil {
		http.Error(resp, err.Error(), http.StatusInternalServerError)
		return
	}

	defer destFile.Close()

	_, err = io.Copy(destFile, file)
	if err != nil {
		http.Error(resp, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Printf("Final Filename: %s\n", finalfilename)
	fmt.Printf("Final Filepath: %s\n", finalFilepath)
	fmt.Printf("---------------------------\n")

	// Create task record
	err = dbStore.CreateTask(opID)
	if err != nil {
		http.Error(resp, "Failed to create task", http.StatusInternalServerError)
		return
	}

	// Submit async job to worker pool
	job := InvoiceJob{
		FilePath:    finalFilepath,
		OperationID: opID,
	}

	err = workerPool.Submit(func() {
		processInvoiceAsync(job)
	})
	if err != nil {
		http.Error(resp, "Failed to submit job to worker pool", http.StatusInternalServerError)
		return
	}

	// Return 202 Accepted with operation ID
	resp.Header().Set("Content-Type", "application/json")
	resp.Header().Set("Location", fmt.Sprintf("/documentintelligence/documentModels/prebuilt-invoice/analyzeResults/%s", opID))
	resp.WriteHeader(http.StatusAccepted)

	responseBody := map[string]interface{}{
		"operationId": opID,
		"status":      "processing",
	}
	json.NewEncoder(resp).Encode(responseBody)
}

// processInvoiceAsync processes the invoice PDF asynchronously
func processInvoiceAsync(job InvoiceJob) {
	opID := job.OperationID
	filePath := job.FilePath

	fmt.Printf("[%s] Starting async processing...\n", opID)

	var pagenum int = 0
	outputpath := ""
	isFile := false

	// Convert PDF to base64 images
	base64Imgs, err := ConvertPdfToBase64Image(filePath, pagenum, &outputpath, &isFile)
	if err != nil {
		fmt.Printf("[%s] PDF conversion failed: %v\n", opID, err)
		dbStore.UpdateTask(opID, "failed", nil, err.Error())
		return
	}

	// Call Ollama OCR model
	extracted, err := CallOllamaOCRModel(base64Imgs)
	if err != nil {
		fmt.Printf("[%s] Ollama model processing failed: %v\n", opID, err)
		dbStore.UpdateTask(opID, "failed", nil, err.Error())
		return
	}

	// Build Azure-compatible response
	azureMockData := AnalyzeResult{
		Documents: []AzureDocument{
			{
				DocType: "invoice",
				Fields: map[string]AzureField{
					"VendorName":    {Type: "string", ValueString: extracted["SupplierName"], Content: extracted["SupplierName"]},
					"InvoiceTotal":  {Type: "string", ValueString: extracted["TotalAmount"], Content: extracted["TotalAmount"]},
					"ValueAddedTax": {Type: "string", ValueString: extracted["VAT"], Content: extracted["VAT"]},
					"Tax":           {Type: "string", ValueString: extracted["Tax"], Content: extracted["Tax"]},
				},
			},
		},
	}

	responsePayload := AzureSyncResponse{
		ApiVersion:      "2024-11-30",
		Status:          "succeeded",
		CreatedDateTime: time.Now().Format(time.RFC3339),
		AnalyzeResult:   azureMockData,
	}

	// Update task with success
	fmt.Printf("[%s] Extraction succeeded!\n", opID)
	dbStore.UpdateTask(opID, "succeeded", responsePayload, "")
}

// handleGetAnalyzeResults retrieves the status and results of an analysis task
func handleGetAnalyzeResults(resp http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(resp, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	opID := req.PathValue("opID")
	if opID == "" {
		http.Error(resp, "Operation ID is required", http.StatusBadRequest)
		return
	}

	task, err := dbStore.GetTask(opID)
	if err != nil {
		http.Error(resp, err.Error(), http.StatusNotFound)
		return
	}

	resp.Header().Set("Content-Type", "application/json")
	resp.WriteHeader(http.StatusOK)
	json.NewEncoder(resp).Encode(task)
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
