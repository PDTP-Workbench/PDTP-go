package pdtp

import (
	"errors"
	"io"
	"net/http"
)

type CompressionMethod interface {
	Name() string
	Writer(w http.ResponseWriter) (FlusherWriter, error)
}

// FlusherWriterはWrite, Flush, Closeを持つインターフェイス
type FlusherWriter interface {
	io.Writer
	Flush() error
	Close() error
}

// TODO: 圧縮しない場合の処理を追加
func CompressionMiddleware(w http.ResponseWriter, r *http.Request, comp CompressionMethod) (FlusherWriter, http.Flusher, error) {
	// 共通ヘッダ
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	fw, err := comp.Writer(w)
	if err != nil {
		http.Error(w, "Failed to initialize compression", http.StatusInternalServerError)
		return nil, nil, err
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return nil, nil, errors.New("streaming unsupported")
	}

	return fw, flusher, nil
}
