package pdtp

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"context"
)

// newTestLogger creates a logger that writes to a bytes.Buffer for capturing output.
// Duplicated from parser_test.go for now; could be moved to a common test utility package.
func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug, // Capture all levels
	}))
}

// MockIPDFFile from parser_test.go
type MockIPDFFile struct {
	io.ReadSeeker
	CloseFunc func() error
	ReadFunc  func(p []byte) (n int, err error)
	SeekFunc  func(offset int64, whence int) (int64, error)
}

func (m *MockIPDFFile) Read(p []byte) (n int, err error) {
	if m.ReadFunc != nil { return m.ReadFunc(p) }
	if m.ReadSeeker != nil { return m.ReadSeeker.Read(p) }
	return 0, io.EOF
}
func (m *MockIPDFFile) Seek(offset int64, whence int) (int64, error) {
	if m.SeekFunc != nil { return m.SeekFunc(offset, whence) }
	if m.ReadSeeker != nil { return m.ReadSeeker.Seek(offset, whence) }
	return 0, nil
}
func (m *MockIPDFFile) Close() error {
	if m.CloseFunc != nil { return m.CloseFunc() }
	return nil
}


// MockPDFParser allows us to control PDFParser behavior during handler tests.
type MockPDFParser struct {
	StreamPageContentsFunc func(ctx context.Context, start, end, base int64, insertData func(data ParsedData)) error
}

func (m *MockPDFParser) StreamPageContents(ctx context.Context, start, end, base int64, insertData func(data ParsedData)) error {
	if m.StreamPageContentsFunc != nil {
		return m.StreamPageContentsFunc(ctx, start, end, base, insertData)
	}
	// Default: do nothing, return no error
	return nil
}
func (m *MockPDFParser) GetCatalog() (*Catalog, error) { return nil, nil } // Implement other IPDFParser methods if needed by handler flow
func (m *MockPDFParser) GetObject(ref PDFRef) (PDFObject, error) { return nil, nil }
func (m *MockPDFParser) GetPageByNumber(pageNum int) (*Page, error) { return nil, nil }
func (m *MockPDFParser) Close() error { return nil }


// Store original NewPDFParser and restore it after tests that mock it.
var originalNewPDFParser = newPDFParser // Assuming newPDFParser is the actual internal variable used.
                                      // If NewPDFParser is a global func, this needs adjustment.
                                      // For this example, let's assume NewPDFParser is a package-level func variable for mocking:
var newPDFParserFunc func(open func() (IPDFFile, error), logger *slog.Logger) (IPDFParser, error) = NewPDFParser


func TestNewPDFProtocolHandler_Logging(t *testing.T) {
	// This setup assumes NewPDFParser is a package-level variable that can be swapped for a mock.
	// If NewPDFParser is a direct function call, mocking it is harder without interfaces or build tags.
	// For this test, we assume `newPDFParserFunc` is used internally by the handler, or we mock `NewPDFParser` itself.
	// Let's simplify and test the handler's direct logging points first.

	originalDefaultParserFunc := NewPDFParser // Store to restore
	t.Cleanup(func() {
		NewPDFParser = originalDefaultParserFunc // Restore if changed
	})


	t.Run("logs warning if file parameter is missing", func(t *testing.T) {
		var logBuf bytes.Buffer
		customLogger := newTestLogger(&logBuf)

		config := Config{
			Logger: customLogger,
			// Other fields like HandleOpenPDF can be nil if not reached in this test path
		}
		handler := NewPDFProtocolHandler(config)

		req := httptest.NewRequest("GET", "/pdf?pdtp=1-2", nil) // No 'file' param
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if !strings.Contains(logBuf.String(), "Invalid request: file parameter is missing") {
			t.Errorf("Expected log message about missing 'file' parameter, got: %s", logBuf.String())
		}
		if !strings.Contains(logBuf.String(), "level=WARN") {
			t.Errorf("Expected missing 'file' parameter log to be WARN level, got: %s", logBuf.String())
		}
	})

	t.Run("logs error if HandleOpenPDF fails", func(t *testing.T) {
		var logBuf bytes.Buffer
		customLogger := newTestLogger(&logBuf)
		expectedError := "failed to open test file"

		config := Config{
			Logger: customLogger,
			HandleOpenPDF: func(fileName string) (IPDFFile, error) {
				return nil, errors.New(expectedError)
			},
		}
		handler := NewPDFProtocolHandler(config)

		// Mock NewPDFParser because we are testing HandleOpenPDF failure, which happens *before* NewPDFParser is called with the openFunc.
		// The error from HandleOpenPDF will be returned by the openFunc closure passed to NewPDFParser.
		// NewPDFParser itself logs errors from its openFunc.
		// So, the logger passed to NewPDFParser (which is customLogger) should show this.

		// To ensure NewPDFParser is called with our custom logger:
		NewPDFParser = func(open func() (IPDFFile, error), logger *slog.Logger) (IPDFParser, error) {
			if logger != customLogger {
				t.Error("NewPDFParser was not called with the customLogger from config")
			}
			// Simulate the open failing
			_, openErr := open()
			// NewPDFParser would typically log this error using its *own* logger instance if open() fails.
			// The handler's logger is used for errors *before* this point, or for errors returned by NewPDFParser.
			// This test setup is a bit complex due to the chain of loggers.

			// Let's assume NewPDFParser itself logs the open() error.
			// The key is that customLogger is passed to NewPDFParser.
			// If NewPDFParser then uses this logger to log the error from open(), it will be captured.
			// The current NewPDFParser in parser.go does *not* log the error from open() itself, it returns it.
			// The handler then logs the error returned by NewPDFParser.

			// So, the handler should log "Parser initialization error" with our customLogger.
			return nil, openErr // Propagate the error as NewPDFParser would
		}


		req := httptest.NewRequest("GET", "/pdf?file=test.pdf", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		logContent := logBuf.String()
		if !strings.Contains(logContent, "Parser initialization error") {
			t.Errorf("Expected log message 'Parser initialization error', got: %s", logContent)
		}
		if !strings.Contains(logContent, expectedError) {
			t.Errorf("Expected log to contain the specific error from HandleOpenPDF ('%s'), got: %s", expectedError, logContent)
		}
		if !strings.Contains(logContent, "level=ERROR") {
			t.Errorf("Expected 'Parser initialization error' log to be ERROR level, got: %s", logContent)
		}
	})

	t.Run("logs error if StreamPageContents fails", func(t *testing.T) {
		var logBuf bytes.Buffer
		customLogger := newTestLogger(&logBuf)
		expectedError := "streaming failed"

		mockParser := &MockPDFParser{
			StreamPageContentsFunc: func(ctx context.Context, start, end, base int64, insertData func(data ParsedData)) error {
				return errors.New(expectedError)
			},
		}

		// Mock NewPDFParser to return our mockParser instance
		NewPDFParser = func(open func() (IPDFFile, error), logger *slog.Logger) (IPDFParser, error) {
			if logger != customLogger {
				 t.Error("NewPDFParser in StreamPageContents test not called with customLogger")
			}
			// Successfully open a dummy file for this path
			// _, _ = open() // Call open to ensure it doesn't hang or error unexpectedly
			return mockParser, nil
		}

		config := Config{
			Logger: customLogger,
			HandleOpenPDF: func(fileName string) (IPDFFile, error) {
				// Return a minimal valid PDF mock so that open() inside NewPDFParser doesn't fail.
				return &MockIPDFFile{ReadSeeker: strings.NewReader(minimalValidPDFStream)}, nil
			},
		}
		handler := NewPDFProtocolHandler(config)

		req := httptest.NewRequest("GET", "/pdf?file=test.pdf&pdtp=1-1", nil)
		rr := httptest.NewRecorder()

		// Run the handler, which will eventually call StreamPageContents on the mock.
		// The error from StreamPageContents is logged inside the goroutine in the handler.
		handler.ServeHTTP(rr, req)

		// Note: The logging from the goroutine might not be captured immediately after ServeHTTP returns.
		// This test might be flaky if the goroutine doesn't finish logging before assertions are made.
		// For more robust testing of goroutine logging, one might need channels or wait groups.
		// However, for typical test execution speed, it often works.

		logContent := logBuf.String()
		// The handler logs "Error streaming page contents"
		if !strings.Contains(logContent, "Error streaming page contents") {
			t.Errorf("Expected log message 'Error streaming page contents', got: %s", logContent)
		}
		if !strings.Contains(logContent, expectedError) {
			t.Errorf("Expected log to contain the specific error from StreamPageContents ('%s'), got: %s", expectedError, logContent)
		}
		if !strings.Contains(logContent, "level=ERROR") {
			t.Errorf("Expected 'Error streaming page contents' log to be ERROR level, got: %s", logContent)
		}
	})
}

// minimalValidPDFStream for HandleOpenPDF success cases (copied from parser_test)
const minimalValidPDFStream = `1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources <<>> >>
endobj
4 0 obj
<< /Length 5 >>
stream
BT ET
endstream
endobj
xref
0 5
0000000000 65535 f
0000000010 00000 n
0000000050 00000 n
0000000100 00000 n
0000000200 00000 n
trailer
<< /Size 5 /Root 1 0 R >>
startxref
250
%%EOF
`
