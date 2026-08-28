package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

func main() {
	http.HandleFunc("GET /health", handleHealthCheck)
	http.HandleFunc("/documentintelligence/documentModels/prebuilt-invoice:analyze", handleAnalyzePOST)

	var port int = 8082
	fmt.Printf("Server starting at http://localhost:%d\n", port)
	if serverErr := http.ListenAndServe(":8082", nil); serverErr != nil {
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

	var pagenum int
	pagenum = 0
	// outputpath := fmt.Sprintf("./output/page-%d.jpg", pagenum)
	// isFile := true
	outputpath := ""
	isFile := false
	base64Img, err := ConvertPdfToBase64Image(finalFilepath, pagenum, &outputpath, &isFile)
	if err != nil {
		http.Error(resp, err.Error(), http.StatusInternalServerError)
		return
	}

	var base64ImgDataUrl string
	if isFile {

		base64ImgDataUrl = base64Img
	} else {

		base64ImgDataUrl = "data:image/jpeg;base64," + base64Img
	}

	fmt.Printf("Pdf firstpage converts to base64image string: %s", base64ImgDataUrl)
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
