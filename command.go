package pdtp

// CommandType は描画コマンドの種別を示す
type CommandType int

const (
	CommandTypeText CommandType = iota
	CommandTypeImage
)

type TextCommand struct {
	X        float64  // X座標
	Y        float64  // Y座標
	Z        int64    // Z座標
	Text     []string // テキストの生バイト列
	FontID   string   // フォントID
	FontSize float64  // フォントサイズ
}

type ImageCommand struct {
	X       float64 // X座標
	Y       float64 // Y座標
	Z       int64   // Z座標
	Width   float64 // 幅
	Height  float64 // 高さ
	ImageID string  // 画像ID
}

type IDrawCommand interface {
	GetTextCommand() *[]TextCommand
	GetImageCommand() *[]ImageCommand
}

type DrawCommand struct {
	contents      string
	textCommands  []TextCommand
	imageCommands []ImageCommand
}

func (dc *DrawCommand) GetTextCommand() *[]TextCommand {
	return &dc.textCommands
}

func (dc *DrawCommand) GetImageCommand() *[]ImageCommand {
	return &dc.imageCommands
}
