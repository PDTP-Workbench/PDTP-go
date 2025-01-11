package pdtp

import (
	"net/http"

	"github.com/klauspost/compress/zstd"
)

type ZstdFlusherWriter struct {
	zw *zstd.Encoder
}

func (z *ZstdFlusherWriter) Write(p []byte) (int, error) {
	return z.zw.Write(p)
}

func (z *ZstdFlusherWriter) Flush() error {
	return z.zw.Flush()
}

func (z *ZstdFlusherWriter) Close() error {
	return z.zw.Close()
}
func (z ZstdCompression) Writer(w http.ResponseWriter) (FlusherWriter, error) {
	w.Header().Set("Content-Encoding", "zstd")
	zw, err := zstd.NewWriter(w)
	if err != nil {
		return nil, err
	}
	return &ZstdFlusherWriter{zw: zw}, nil
}

type ZstdCompression struct{}

func (z ZstdCompression) Name() string {
	return "zstd"
}
