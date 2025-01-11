package pdtp

import (
	"compress/gzip"
	"net/http"
)

type GzipCompression struct{}

func (g GzipCompression) Name() string {
	return "gzip"
}

func (g GzipCompression) Writer(w http.ResponseWriter) (FlusherWriter, error) {
	w.Header().Set("Content-Encoding", "gzip")
	gz := gzip.NewWriter(w)
	hf, ok := w.(http.Flusher)
	if !ok {
		return nil, nil
	}
	// TODO: /n
	return &GzipFlusherWriter{gz: gz, hf: hf}, nil
}

type GzipFlusherWriter struct {
	gz *gzip.Writer
	hf http.Flusher
}

// TODO: gfw
func (g *GzipFlusherWriter) Write(p []byte) (int, error) {
	return g.gz.Write(p)
}

func (g *GzipFlusherWriter) Flush() error {
	err := g.gz.Flush()
	if err != nil {
		return err
	}
	g.hf.Flush()
	return nil
}

func (g *GzipFlusherWriter) Close() error {
	return g.gz.Close()
}
