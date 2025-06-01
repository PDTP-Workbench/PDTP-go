package pdtp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// FIXME:configにLoggerを加える場合の設計
type Config struct {
	CompressionMethod CompressionMethod
	HandleOpenPDF     func(fileName string) (IPDFFile, error)
	Logger            *slog.Logger
}

func NewPDFProtocolHandler(config Config) http.HandlerFunc {

	return func(w http.ResponseWriter, r *http.Request) {
		logger := config.Logger
		if logger == nil {
			logger = slog.Default()
		}
		fw, flusher, err := CompressionMiddleware(w, r, config.CompressionMethod)
		if err != nil {
			logger.Error("Compression error", "error", err)
		}

		fileName := r.URL.Query().Get("file")
		if fileName == "" { // Removed err check here as it's already covered by CompressionMiddleware
			logger.Warn("Invalid request: file parameter is missing")
			return
		}
		pdtpField := r.Header.Get("pdtp")

		start, end, base, err := parsePDTPField(pdtpField)

		outCh := make(chan ParsedData, 20)
		defer close(outCh)

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		pp, err := NewPDFParser(func() (IPDFFile, error) {
			file, err := config.HandleOpenPDF(fileName)
			if err != nil {
				return nil, err
			}
			return file, nil
		}, logger)
		if err != nil {
			logger.Error("Parser initialization error", "error", err)
			return
		}

		go func() {
			err := pp.StreamPageContents(ctx, start, end, base, func(data ParsedData) {
				outCh <- data
			})
			if err != nil {
				// TODO: slogでログレベルを使ってログ出力
				// 解析エラーの場合はエラーチャンク送信 or ログ出力
				logger.Error("Error streaming page contents", "error", err)
				return
			}
			return
		}()

		// The SendChunkIter error was a TODO and not actual running code.
		// If it were, it would be:
		// if err != nil {
		// 	 logger.Error("SendChunkIter error", "error", err)
		// 	 return
		// }

		// チャンク送信
		for d := range outCh {
			// Pass logger to sendChunk
			sendChunk(d, fw, flusher, logger)
		}
	}
}

func sendChunk(data ParsedData, fw FlusherWriter, flusher http.Flusher, logger *slog.Logger) error {
	switch d := data.(type) {
	case *ParsedPage:
		chunk := NewPageChunk(&NewPageChunkArgs{
			Width:  d.Width,
			Height: d.Height,
			Page:   d.Page,
		},
		)

		if err := chunk.Send(fw, flusher); err != nil {
			return err
		}
	case *ParsedText:
		chunk := NewTextChunk(
			&TextChunkArgs{X: d.X,
				Y:        d.Y,
				Z:        d.Z,
				Text:     d.Text,
				FontID:   d.FontID,
				FontSize: d.FontSize,
				Page:     d.Page,
				Color:    d.Color,
			},
		)
		if err := chunk.Send(fw, flusher); err != nil {
			logger.Warn("SendTextChunk error", "error", err)
			return err
		}

	case *ParsedImage:
		chunk := NewImageChunk(&ImageChunkArgs{
			X:        d.X,
			Y:        d.Y,
			Z:        d.Z,
			Width:    d.Width,
			Height:   d.Height,
			DW:       d.DW,
			DH:       d.DH,
			Page:     d.Page,
			Data:     d.Data,
			MaskData: d.MaskData,
			Ext:      d.Ext,
			ClipPath: d.ClipPath,
		})

		if err := chunk.Send(fw, flusher); err != nil {
			return err
		}

	case *ParsedFont:
		newFont, err := fixOS2Table(d.Data)
		if err != nil {
			logger.Warn("fixOS2Table error", "error", err)
		}
		chunk := NewFontChunk(&FontChunkArgs{
			FontID: d.FontID,
			Font:   newFont,
		})
		if err := chunk.Send(fw, flusher); err != nil {
			return err
		}
	case *ParsedPath:
		chunk := NewPathChunk(&PathChunkArgs{
			X:           d.X,
			Y:           d.Y,
			Z:           d.Z,
			Width:       d.Width,
			Height:      d.Height,
			Page:        d.Page,
			FillColor:   d.FillColor,
			StrokeColor: d.StrokeColor,
			Path:        d.Path,
		})

		if err := chunk.Send(fw, flusher); err != nil {
			return err
		}
	}

	return nil
}

// PDTP: “start=1;end=10;base=1;”
// base: 読みこみ基準ページ
// 		初期値: 1
// start: 読み込み範囲最小ページ
// 		初期値: 1
// end:   読み込み範囲最大ページ
// 		初期値: PDFのページ数

func parsePDTPField(pdtpField string) (int64, int64, int64, error) {
	var start, end, base int64
	start = 1
	base = 1
	end = -1
	if pdtpField == "" {
		return start, end, base, nil
	}
	pdtpField = strings.Trim(pdtpField, ";")
	fields := strings.Split(pdtpField, ";")
	for _, field := range fields {
		kv := strings.Split(field, "=")
		if len(kv) != 2 {
			return start, end, base, fmt.Errorf("Invalid pdtp field")
		}
		switch kv[0] {
		case "start":
			start, _ = strconv.ParseInt(kv[1], 10, 32)
		case "end":
			end, _ = strconv.ParseInt(kv[1], 10, 32)
		case "base":
			base, _ = strconv.ParseInt(kv[1], 10, 32)
		default:
			return start, end, base, fmt.Errorf("Invalid pdtp field")
		}
	}
	return start, end, base, nil
}
