package pdtp

type ParsedDataType int

// ParsedData インターフェース: 解析結果(テキスト/画像/フォント)を表す
type ParsedData interface {
}

// --------------------------
// ページデータ
// --------------------------
type ParsedPage struct {
	Width  float64
	Height float64
	Page   int64
}

// --------------------------
// テキストデータ
// --------------------------
type ParsedText struct {
	X        float64
	Y        float64
	Z        int64
	Text     string
	FontID   string
	FontSize float64
	Page     int64
}

// --------------------------
// 画像データ
// --------------------------
type ParsedImage struct {
	X        float64
	Y        float64
	Z        int64
	Width    float64
	Height   float64
	Data     []byte // 解凍済み画像バイト列
	MaskData []byte // 解凍済みマスクバイト列
	Page     int64
}

// --------------------------
// フォントファイルデータ
// --------------------------
type ParsedFont struct {
	FontID string
	Data   []byte // フォントファイル本体
}
