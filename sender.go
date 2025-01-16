package pdtp

import (
	"encoding/binary"
	"encoding/json"
	"log"
	"net/http"
)

const (
	DataTypePage  = byte(0x00)
	DataTypeText  = byte(0x01)
	DataTypeImage = byte(0x02)
	DataTypeFont  = byte(0x03)
	DataTypePath  = byte(0x04)
	DataTypeError = byte(0xFF)
)

type IChunk interface {
	Send(w FlusherWriter, flusher http.Flusher) error
}

type PageChunk struct {
	IChunk

	json *NewPageChunkArgs
}

type NewPageChunkArgs struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Page   int64   `json:"page"`
}

func NewPageChunk(args *NewPageChunkArgs) *PageChunk {
	return &PageChunk{
		json: args,
	}
}

func (p *PageChunk) Send(w FlusherWriter, flusher http.Flusher) error {
	jsonData, err := json.Marshal(&p.json)
	if err != nil {
		return err
	}
	messageType := DataTypePage
	messageLength := uint32(len(jsonData))
	messageData := jsonData
	lengthBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBuf, messageLength)
	if _, err := w.Write([]byte{messageType}); err != nil {
		log.Printf("Failed to write message length: %v", err)
		return err
	}

	if _, err := w.Write(lengthBuf); err != nil {
		log.Printf("Failed to write message length: %v", err)
		return err
	}

	if _, err := w.Write(messageData); err != nil {
		log.Printf("Failed to write message messageLength: %v", err)
		return err
	}

	w.Flush()
	flusher.Flush()

	return nil
}

type TextChunkArgs struct {
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Z        int64   `json:"z"`
	Text     string  `json:"text"`
	FontID   string  `json:"fontID"`
	FontSize float64 `json:"fontSize"`
	Page     int64   `json:"page"`
}

type TextChunk struct {
	IChunk

	json *TextChunkArgs
}

func NewTextChunk(args *TextChunkArgs) *TextChunk {
	return &TextChunk{
		json: args,
	}
}

func (p *TextChunk) Send(w FlusherWriter, flusher http.Flusher) error {
	jsonData, err := json.Marshal(&p.json)
	if err != nil {
		return err
	}
	messageType := DataTypeText
	messageLength := uint32(len(jsonData))
	messageData := jsonData
	lengthBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBuf, messageLength)
	if _, err := w.Write([]byte{messageType}); err != nil {
		log.Printf("Failed to write message length: %v", err)
		return err
	}

	if _, err := w.Write(lengthBuf); err != nil {
		log.Printf("Failed to write message length: %v", err)
		return err
	}

	if _, err := w.Write(messageData); err != nil {
		log.Printf("Failed to write message messageLength: %v", err)
		return err
	}

	w.Flush()
	flusher.Flush()

	return nil
}

type ImageChunkArgs struct {
	X        float64
	Y        float64
	Z        int64
	Width    float64
	Height   float64
	DW       float64
	DH       float64
	Data     []byte
	MaskData []byte
	Page     int64
	Ext      string
}

type ImageChunk struct {
	IChunk

	json     *SendImageJson
	Data     *[]byte
	MaskData *[]byte
}

type SendImageJson struct {
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Z          int64   `json:"z"`
	Width      float64 `json:"width"`
	Height     float64 `json:"height"`
	DW         float64 `json:"dw"`
	DH         float64 `json:"dh"`
	Length     int64   `json:"length"`
	MaskLength int64   `json:"maskLength"`
	Page       int64   `json:"page"`
	Ext        string  `json:"ext"`
}

func NewImageChunk(args *ImageChunkArgs) *ImageChunk {
	return &ImageChunk{
		json: &SendImageJson{
			X:          args.X,
			Y:          args.Y,
			Z:          args.Z,
			Width:      args.Width,
			Height:     args.Height,
			DW:         args.DW,
			DH:         args.DH,
			Length:     int64(len(args.Data)),
			MaskLength: int64(len(args.MaskData)),
			Page:       args.Page,
			Ext:        args.Ext,
		},
		Data:     &args.Data,
		MaskData: &args.MaskData,
	}
}

func (p *ImageChunk) Send(w FlusherWriter, flusher http.Flusher) error {
	jsonData, err := json.Marshal(&p.json)
	if err != nil {
		return err
	}
	messageType := DataTypeImage
	messageLength := uint32(len(jsonData))
	messageData := jsonData
	messageData = append(messageData, *p.Data...)
	messageData = append(messageData, *p.MaskData...)

	lengthBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBuf, messageLength)
	if _, err := w.Write([]byte{messageType}); err != nil {
		log.Printf("Failed to write message length: %v", err)
		return err
	}

	if _, err := w.Write(lengthBuf); err != nil {
		log.Printf("Failed to write message length: %v", err)
		return err
	}

	if _, err := w.Write(messageData); err != nil {
		log.Printf("Failed to write message messageLength: %v", err)
		return err
	}
	w.Flush()
	flusher.Flush()
	return nil
}

type FontChunkArgs struct {
	FontID string
	Font   []byte
}

type FontChunk struct {
	IChunk

	json *SendFontJson
	Font *[]byte
}

type SendFontJson struct {
	FontID string
	Length int64
}

func NewFontChunk(args *FontChunkArgs) *FontChunk {
	return &FontChunk{
		json: &SendFontJson{
			FontID: args.FontID,
			Length: int64(len(args.Font)),
		},
		Font: &args.Font,
	}
}

func (p *FontChunk) Send(w FlusherWriter, flusher http.Flusher) error {
	jsonData, err := json.Marshal(&p.json)
	if err != nil {
		return err
	}
	messageType := DataTypeFont
	messageLength := uint32(len(jsonData))
	messageData := jsonData
	messageData = append(messageData, *p.Font...)

	lengthBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBuf, messageLength)
	if _, err := w.Write([]byte{messageType}); err != nil {
		log.Printf("Failed to write message length: %v", err)
		return err
	}

	if _, err := w.Write(lengthBuf); err != nil {
		log.Printf("Failed to write message length: %v", err)
		return err
	}

	if _, err := w.Write(messageData); err != nil {
		log.Printf("Failed to write message messageLength: %v", err)
		return err
	}
	w.Flush()
	flusher.Flush()
	return nil
}

type PathChunkArgs struct {
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Z           int64   `json:"z"`
	Width       float64 `json:"width"`
	Height      float64 `json:"height"`
	Page        int64   `json:"page"`
	Path        string  `json:"path"`
	FillColor   string  `json:"fillColor"`
	StrokeColor string  `json:"strokeColor"`
}

type PathChunk struct {
	IChunk

	json *PathChunkArgs
}

func NewPathChunk(args *PathChunkArgs) *PathChunk {
	return &PathChunk{
		json: args,
	}
}

func (p *PathChunk) Send(w FlusherWriter, flusher http.Flusher) error {
	jsonData, err := json.Marshal(&p.json)
	if err != nil {
		return err
	}
	messageType := DataTypePath
	messageLength := uint32(len(jsonData))
	messageData := jsonData
	lengthBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lengthBuf, messageLength)
	if _, err := w.Write([]byte{messageType}); err != nil {
		log.Printf("Failed to write message length: %v", err)
		return err
	}

	if _, err := w.Write(lengthBuf); err != nil {
		log.Printf("Failed to write message length: %v", err)
		return err
	}

	if _, err := w.Write(messageData); err != nil {
		log.Printf("Failed to write message messageLength: %v", err)
		return err
	}

	w.Flush()
	flusher.Flush()

	return nil
}

type ErrorChunk struct {
	IChunk

	Code    int
	Message string
}

func (p *ErrorChunk) Send(w FlusherWriter, flusher http.Flusher, code int, message string) error {
	return nil
}
