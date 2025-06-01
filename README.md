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
	// "log" // Standard log can be replaced by slog
	"log/slog" // Import slog
	"net/http"
	"os"

	"github.com/pdtp-workbench/pdtp-go" // Make sure this import path is correct
)

func main() {
	// For the basic example, we can use slog's default logger or a simple one.
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	http.HandleFunc("/pdtp", pdtp.NewPDFProtocolHandler(
		pdtp.Config{
			HandleOpenPDF: func(fileName string) (pdtp.IPDFFile, error) {
				file, err := os.Open(fileName)
				// Basic error logging for the example itself
				if err != nil {
					logger.Error("HandleOpenPDF failed to open", "filename", fileName, "error", err)
				}
				return file, err
			},
			CompressionMethod: pdtp.ZstdCompression{},
			// Logger: logger, // Optionally set the logger for the library too
		},
	))

	logger.Info("PDF Protocol Server listening", "address", "http://localhost:8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		logger.Error("Server failed to start", "error", err)
		os.Exit(1) // Mimic log.Fatal behavior
	}
}
```

### Logging

This library uses the standard Go `log/slog` package for logging.
You can customize the logging behavior by providing your own `*slog.Logger` instance through the `Config` struct.

By default, if no logger is specified, `slog.Default()` will be used.

**Example: Setting a custom logger**

```go
package main

import (
	"fmt"
	// "log" // Standard log is not used here directly
	"log/slog" // Import slog
	"net/http"
	"os"

	"github.com/pdtp-workbench/pdtp-go" // Make sure this import path is correct
)

func main() {
	// Prepare a custom logger (e.g., writing JSON to stderr with Debug level)
	jsonHandler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
	customLogger := slog.New(jsonHandler)

	config := pdtp.Config{
		HandleOpenPDF: func(fileName string) (pdtp.IPDFFile, error) {
			file, err := os.Open(fileName)
			if err != nil {
				// Log the error using your custom logger if needed,
				// though the library will also log errors during parsing.
				customLogger.Error("Failed to open PDF in HandleOpenPDF", "filename", fileName, "error", err)
				return nil, err
			}
			return file, nil
		},
		CompressionMethod: pdtp.ZstdCompression{},
		Logger:            customLogger, // Set the custom logger here
	}

	http.HandleFunc("/pdtp", pdtp.NewPDFProtocolHandler(config))

	customLogger.Info("PDF Protocol Server listening", "address", "http://localhost:8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		customLogger.Error("Server failed to start", "error", err)
	}
}

```
The library logs at different levels:
- `ERROR`: For critical errors that prevent further processing (e.g., unable to open or parse the PDF file itself, XRef table issues).
- `WARN`: For non-critical errors where processing can continue but some parts might be missing or incorrect (e.g., failure to parse a specific image stream or font data within a page).
- `INFO`: For general informational messages (currently less used by the library itself, but your custom logger can be configured to show these).
- `DEBUG`: For detailed debugging information (currently less used by the library itself).

You can configure your `slog.Handler` to filter logs by these levels as needed.

## License

MIT License
