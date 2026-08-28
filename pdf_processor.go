package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image/jpeg"
	"os"

	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
)

func ConvertPdfToBase64Image(pdfpath string, pagenum int, outputPath *string, isFile *bool) (string, error) {
	pool, err := webassembly.Init(webassembly.Config{
		MinIdle:  1,
		MaxIdle:  2,
		MaxTotal: 5,
	})

	if err != nil {
		return "", fmt.Errorf("failed to init pdfium wasm pool: %w", err)
	}

	defer pool.Close()

	instance, err := pool.GetInstanceWithContext(context.Background())

	if err != nil {
		return "", fmt.Errorf("failed to get pdfium instance: %w", err)
	}

	defer instance.Close()

	pdfbytes, err := os.ReadFile(pdfpath)
	if err != nil {
		return "", fmt.Errorf("Pdf open failed: %w", err)
	}

	opendoc, err := instance.OpenDocument(&requests.OpenDocument{
		File: &pdfbytes,
	})
	if err != nil {
		return "", fmt.Errorf("pdf opening failed through pdfium: %w", err)
	}

	defer instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{
		Document: opendoc.Document,
	})

	renderedPage, err := instance.RenderPageInDPI(&requests.RenderPageInDPI{
		Page: requests.Page{
			ByIndex: &requests.PageByIndex{
				Document: opendoc.Document,
				Index:    pagenum,
			},
		},
		DPI: 150,
	})

	if err != nil {
		return "", fmt.Errorf("failed to render the pdf as image: %w", err)
	}

	defer renderedPage.Cleanup()

	if !*isFile {

		var jpegimgplaceholder bytes.Buffer

		src := renderedPage.Result.RenderedImage
		err = jpeg.Encode(&jpegimgplaceholder, src, &jpeg.Options{
			Quality: 90,
		})

		if err != nil {
			return "", fmt.Errorf("failed to encode JPEG: %w", err)
		}

		return base64.StdEncoding.EncodeToString(jpegimgplaceholder.Bytes()), nil
	} else {

		if err := os.MkdirAll("./output", 0775); err != nil {
			return "", fmt.Errorf("failed to create output directory: %w", err)
		}

		// Render directly to JPEG.
		_, err = instance.RenderToFile(&requests.RenderToFile{
			RenderPageInDPI: &requests.RenderPageInDPI{
				Document: &opendoc.Document,
				Page: requests.Page{
					ByIndex: &requests.PageByIndex{
						Document: opendoc.Document,
						Index:    pagenum,
					},
				},
				DPI: 150,
			},
			OutputFormat:   requests.RenderToFileOutputFormatJPG,
			OutputTarget:   requests.RenderToFileOutputTargetFile,
			OutputQuality:  90,
			TargetFilePath: *outputPath,
		})
		if err != nil {
			return "", fmt.Errorf("failed to render PDF page to JPEG: %w", err)
		}

		return *outputPath, nil
	}
}
