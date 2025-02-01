# PDTP Go

PDTP Go is a Go package that provides a simple way to handle PDF protocol transfers.
It enables you to create a PDF protocol server with customizable file handling and compression support.

## Installation

Install the package via `go get`:

```bash
go get github.com/pdtp-workbench/pdtp-go
```

## Usage

Below is a basic example demonstrating how to use PDTP Go.
This example sets up an HTTP server with a PDF protocol handler that opens a PDF file and applies Zstd compression.

```go
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/pdtp-workbench/pdtp-go"
)

func main() {
	http.HandleFunc("/pdtp", pdtp.NewPDFProtocolHandler(
		pdtp.Config{
			HandleOpenPDF: func(fileName string) (pdtp.IPDFFile, error) {
				file, err := os.Open(fileName)
				return file, err
			},
			CompressionMethod: pdtp.ZstdCompression{},
		},
	))

	fmt.Println("PDF Protocol Server listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
```

## License

MIT License
