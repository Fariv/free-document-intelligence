package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"os"

	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
)

func encodeImageToBase64Slices(filePath string) ([]string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("image open failed: %w", err)
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("image decode failed: %w", err)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		return nil, fmt.Errorf("image jpeg encode failed: %w", err)
	}
	return []string{base64.StdEncoding.EncodeToString(buf.Bytes())}, nil
}

func ConvertPdfToBase64Image(pdfpath string, pagenum int, outputPath *string, isFile *bool) ([]string, error) {
	pool, err := webassembly.Init(webassembly.Config{
		MinIdle:  1,
		MaxIdle:  2,
		MaxTotal: 5,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to init pdfium wasm pool: %w", err)
	}

	defer pool.Close()

	instance, err := pool.GetInstanceWithContext(context.Background())

	if err != nil {
		return nil, fmt.Errorf("failed to get pdfium instance: %w", err)
	}

	defer instance.Close()

	pdfbytes, err := os.ReadFile(pdfpath)
	if err != nil {
		return nil, fmt.Errorf("Pdf open failed: %w", err)
	}

	opendoc, err := instance.OpenDocument(&requests.OpenDocument{
		File: &pdfbytes,
	})
	if err != nil {
		return nil, fmt.Errorf("pdf opening failed through pdfium: %w", err)
	}

	defer instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{
		Document: opendoc.Document,
	})

	pagecountResp, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{
		Document: opendoc.Document,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get page count: %w", err)
	}

	var base64Images []string
	for pagenum = 0; pagenum < pagecountResp.PageCount; pagenum++ {

		if !*isFile {

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
				return nil, fmt.Errorf("failed to render the pdf page %d as image: %w", pagenum, err)
			}

			var jpegimgplaceholder bytes.Buffer

			src := renderedPage.Result.RenderedImage
			err = jpeg.Encode(&jpegimgplaceholder, src, &jpeg.Options{
				Quality: 90,
			})

			if err != nil {
				return nil, fmt.Errorf("failed to encode JPEG from pdf page %d: %w", pagenum, err)
			}

			base64Images = append(base64Images, base64.StdEncoding.EncodeToString(jpegimgplaceholder.Bytes()))

			defer renderedPage.Cleanup()
		} else {

			if err := os.MkdirAll("./output", 0775); err != nil {
				return nil, fmt.Errorf("failed to create output directory for pdf page %d: %w", pagenum, err)
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
				return nil, fmt.Errorf("failed to render PDF page %d to JPEG: %w", pagenum, err)
			}

			base64Images = append(base64Images, *outputPath)
		}
	}

	return base64Images, nil
}
