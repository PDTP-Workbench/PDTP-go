package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/pdtp-workbench/pdtp-go"
)

func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Pdtp")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
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
	http.HandleFunc("/default", func(w http.ResponseWriter, r *http.Request) {
		file := r.URL.Query().Get("file")
		http.ServeFile(w, r, file)
	})

	corsHandler := CORSMiddleware(http.DefaultServeMux)

	fmt.Println("PDF Protocol Server listening on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", corsHandler))
}
