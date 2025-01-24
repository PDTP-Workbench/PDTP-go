package pdtp

import (
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
)

type TokenObject struct {
	fonts    map[string]map[byte]string
	contents string
}

type ITokenObject interface {
	GetFonts() map[byte]string
}

type GraphicsState struct {
	CTM Matrix // 現在の変換マトリックス
}

// 3x3マトリックスを表す構造体
type Matrix [3][3]float64

func NewGraphicsState() *GraphicsState {
	return &GraphicsState{
		CTM: IdentityMatrix(),
	}
}
func ParseFloat(str string) float64 {
	value, err := strconv.ParseFloat(str, 64)
	if err != nil {
		log.Printf("数値に変換できません: %s\n", str)
		return 0
	}
	return value
}

func (m Matrix) Multiply(n Matrix) Matrix {
	var result Matrix
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			sum := 0.0
			for k := 0; k < 3; k++ {
				sum += m[i][k] * n[k][j]
			}
			result[i][j] = sum
		}
	}
	return result
}
func processTJ(arrayContent string, textState *TextState, graphicsState *GraphicsState, currentZ *int64, fonts map[byte]string, colorState ColorState) *TextCommand {

	items, err := parsePDFArray(arrayContent)
	if err != nil {
		fmt.Printf("配列のパースに失敗しました: %v\n", err)
		return nil
	}

	// 最終的なテキストを保持するバッファ
	var finalStrings []string

	for _, item := range items {
		switch v := item.(type) {
		case TextToken:
			finalStrings = append(finalStrings, v...)
		case string:
			// ( ... )形式の文字列なのでparsePDFStringToBytesを適用
			bytes := parsePDFStringToBytes(v, fonts)

			finalStrings = append(finalStrings, bytes...)

		case float64:
			// カーニング処理
			tx := -v / 1000 * textState.FontSize * (textState.HorizontalScaling / 100)
			m := Matrix{
				{1, 0, 0},
				{0, 1, 0},
				{tx, 0, 1},
			}
			textState.Tm = textState.Tm.Multiply(m)
		}
	}
	trm := textState.Tm.Multiply(graphicsState.CTM)
	scaleY := math.Sqrt(trm[1][0]*trm[1][0] + trm[1][1]*trm[1][1])
	effectiveFontSizeY := textState.FontSize * scaleY
	return &TextCommand{
		X:        trm[2][0],
		Y:        trm[2][1],
		Z:        *currentZ,
		Text:     finalStrings,
		FontSize: effectiveFontSizeY,
		FontID:   textState.Font,
		Color:    colorState.FillColor,
	}
}

// テキスト状態を表す構造体
type TextState struct {
	Tm                Matrix  // テキストマトリックス
	Tlm               Matrix  // テキストラインマトリックス
	Font              string  // フォント名
	FontSize          float64 // フォントサイズ
	CharSpacing       float64 // 文字間隔（Tc）
	WordSpacing       float64 // 単語間隔（Tw）
	HorizontalScaling float64 // 水平スケーリング（Th）
	Leading           float64 // リーディング（Tl）
	Rise              float64 // 上昇量（Trise）
}

type ColorState struct {
	StrokeColor string
	FillColor   string
}

func NewColorState() *ColorState {
	return &ColorState{
		StrokeColor: "",
		FillColor:   "",
	}
}

func IdentityMatrix() Matrix {
	return Matrix{
		{1, 0, 0},
		{0, 1, 0},
		{0, 0, 1},
	}
}

// 新しいテキスト状態を作成する関数
func NewTextState() *TextState {
	return &TextState{
		Tm:                IdentityMatrix(),
		Tlm:               IdentityMatrix(),
		FontSize:          12,  // デフォルトのフォントサイズ
		HorizontalScaling: 100, // デフォルトの水平スケーリング（100%）
		Leading:           0,   // デフォルトのリーディング
		Rise:              0,   // デフォルトの上昇量
		CharSpacing:       0,   // デフォルトの文字間隔
		WordSpacing:       0,   // デフォルトの単語間隔
	}
}

type PathState struct {
	X      float64
	Y      float64
	Width  float64
	Height float64
	Path   string
}

func NewPathState() *PathState {
	return &PathState{
		X:      0,
		Y:      0,
		Width:  0,
		Height: 0,
		Path:   "",
	}
}

// sumASCII 関数
func sumASCII(s string) []int {
	sum := make([]int, 0)
	byteS := []byte(s)
	for _, b := range byteS {
		sum = append(sum, int(b))
	}
	return sum
}

// トークンの種類を定義
type TokenType int

const (
	TokenTypeOperator TokenType = iota
	TokenTypeOperand
)

// トークン構造体
type Token struct {
	Value string
	Type  TokenType
}

type TextToken []string
type ByteToken string

func tokenize(content string) ([]Token, error) {
	var tokens []Token
	var currentToken []byte
	inString := false
	inArray := false
	escapeNext := false

	// ここでruneではなくバイトで処理する
	contentBytes := []byte(content)
	for i := 0; i < len(contentBytes); i++ {
		c := contentBytes[i]

		if inString {
			currentToken = append(currentToken, c)
			if escapeNext {
				escapeNext = false
			} else if c == '\\' {
				escapeNext = true
			} else if c == ')' {
				// 文字列終了
				inString = false
				// currentTokenは ( ... ) 含めた生バイト列
				tokens = append(tokens, Token{Value: string(currentToken), Type: TokenTypeOperand})
				currentToken = currentToken[:0]
			}
			continue
		}

		if inArray {
			currentToken = append(currentToken, c)
			if c == ']' {
				inArray = false
				tokens = append(tokens, Token{Value: string(currentToken), Type: TokenTypeOperand})
				currentToken = currentToken[:0]
			}
			continue
		}

		switch c {
		case ' ', '\t', '\r', '\n':
			// トークン区切り
			if len(currentToken) > 0 {
				tokenValue := string(currentToken)
				if isOperator(tokenValue) {
					tokens = append(tokens, Token{Value: tokenValue, Type: TokenTypeOperator})
				} else {
					tokens = append(tokens, Token{Value: tokenValue, Type: TokenTypeOperand})
				}
				currentToken = currentToken[:0]
			}
		case '(':
			// 文字列開始
			inString = true
			currentToken = append(currentToken, c)
		case '[':
			inArray = true
			currentToken = append(currentToken, c)
		default:
			currentToken = append(currentToken, c)
		}
	}

	if len(currentToken) > 0 {
		tokenValue := string(currentToken)
		if isOperator(tokenValue) {
			tokens = append(tokens, Token{Value: tokenValue, Type: TokenTypeOperator})
		} else {
			tokens = append(tokens, Token{Value: tokenValue, Type: TokenTypeOperand})
		}
	}

	return tokens, nil
}

var operators = map[string]bool{
	"q": true, "Q": true, "cm": true, "BT": true, "ET": true,
	"Tf": true, "Tr": true, "Ts": true, "Tw": true, "Tc": true,
	"Tz": true, "TL": true, "Tm": true, "Td": true, "TD": true,
	"T*": true, "'": true, "\"": true, "Tj": true, "TJ": true,
	"Do": true, "w": true, "re": true, "m": true, "l": true,
	"h": true, "f": true, "sc": true, "scn": true, "gs": true,
	"cs": true, "W": true, "n": true, "f*": true, "c": true,
	"SC": true, "M": true, "S": true, "CS": true, "ri": true,
}

func isOperator(s string) bool {
	return operators[s]
}

// ParsePDFArray 関数
func parsePDFArray(arrayStr string) ([]interface{}, error) {
	var items []interface{}
	inString := false
	escapeNext := false
	currentToken := strings.Builder{}
	arrayStr = strings.TrimSpace(arrayStr)

	// 配列の始まりと終わりのブラケットを削除
	if !strings.HasPrefix(arrayStr, "[") || !strings.HasSuffix(arrayStr, "]") {
		return nil, fmt.Errorf("配列の形式が正しくありません: %s", arrayStr)
	}
	content := arrayStr[1 : len(arrayStr)-1]

	contentRunes := []rune(content)
	i := 0
	for i < len(contentRunes) {
		c := contentRunes[i]

		if escapeNext {
			currentToken.WriteRune(c)
			escapeNext = false
			i++
			continue
		}

		if inString {
			currentToken.WriteRune(c)
			if c == '\\' {
				escapeNext = true
			} else if c == ')' {
				inString = false
				items = append(items, currentToken.String())
				currentToken.Reset()
			}
			i++
			continue
		}

		if c == '(' {
			inString = true
			currentToken.WriteRune(c)
			i++
			continue
		}

		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			if currentToken.Len() > 0 {
				tokenStr := currentToken.String()
				if strings.HasPrefix(tokenStr, "<") || strings.HasSuffix(tokenStr, ">") {
					tokenStr = strings.Replace(tokenStr, "<", "", -1)
					tokenStr = strings.Replace(tokenStr, ">", "", -1)

					stringTokens := []string{
						tokenStr[0:4],
						tokenStr[4:8],
					}

					texts := []string{}
					for _, token := range stringTokens {
						t, err := strconv.ParseInt(token, 16, 64)
						if err != nil {
							return nil, fmt.Errorf("16進数のパースに失敗しました: %s", token)
						}
						text := string(rune(t))
						texts = append(texts, text)
					}

					items = append(items, TextToken(texts))
				} else if num, err := strconv.ParseFloat(tokenStr, 64); err == nil {
					items = append(items, num)
				} else {
					return nil, fmt.Errorf("数値のパースに失敗しました: %s", tokenStr)
				}
				currentToken.Reset()
			}
			i++
			continue
		}

		currentToken.WriteRune(c)
		i++
	}

	// 最後のトークンを処理
	if currentToken.Len() > 0 {
		tokenStr := currentToken.String()
		if num, err := strconv.ParseFloat(tokenStr, 64); err == nil {
			items = append(items, num)
		} else {
			return nil, fmt.Errorf("数値のパースに失敗しました: %s", tokenStr)
		}
	}

	return items, nil
}

func (to *TokenObject) processTokens(tokens []Token, pageHeight float64) ([]TextCommand, []ImageCommand, []PathCommand) {
	currentZ := int64(0)
	// グラフィックス状態スタック
	graphicsStack := []*GraphicsState{NewGraphicsState()}
	// テキスト状態
	textState := NewTextState()
	// パス状態
	pathState := NewPathState()
	// カラー状態
	colorState := NewColorState()

	// オペランドスタック
	var operandStack []string
	// テキスト要素のスライス
	var textCommands []TextCommand
	var imageCommands []ImageCommand
	var pathCommands []PathCommand

	// トークンを順番に処理
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		if token.Type == TokenTypeOperand {
			operandStack = append(operandStack, token.Value)
		} else if token.Type == TokenTypeOperator {
			switch token.Value {
			case "q":
				// グラフィックス状態を保存
				currentState := graphicsStack[len(graphicsStack)-1]
				newState := *currentState // シャローコピー
				graphicsStack = append(graphicsStack, &newState)
				operandStack = nil // オペランドスタックをクリア

			case "Q":
				// グラフィックス状態を復元
				if len(graphicsStack) > 1 {
					graphicsStack = graphicsStack[:len(graphicsStack)-1]
				}
				operandStack = nil
			case "cm":
				// CTMを更新
				if len(operandStack) >= 6 {
					a := ParseFloat(operandStack[0])
					b := ParseFloat(operandStack[1])
					c := ParseFloat(operandStack[2])
					d := ParseFloat(operandStack[3])
					e := ParseFloat(operandStack[4])
					f := ParseFloat(operandStack[5])

					m := Matrix{
						{a, b, 0},
						{c, d, 0},
						{e, f, 1},
					}

					currentState := graphicsStack[len(graphicsStack)-1]
					currentState.CTM = currentState.CTM.Multiply(m)
					operandStack = operandStack[6:]
				} else {
					fmt.Println("cm演算子に必要なオペランドが不足しています")
				}
			case "BT":
				// テキストオブジェクトの開始
				textState = NewTextState()
				operandStack = nil
			case "ET":
				// テキストオブジェクトの終了
				operandStack = nil
			case "Tf":
				// フォントとフォントサイズの設定
				if len(operandStack) >= 2 {
					fontName := operandStack[0]
					fontSize := ParseFloat(operandStack[1])
					textState.Font = strings.TrimLeft(fontName, "/")
					textState.FontSize = fontSize
					operandStack = operandStack[2:]
				} else {
					fmt.Println("Tf演算子に必要なオペランドが不足しています")
				}
			case "Tc":
				// 文字間隔の設定
				if len(operandStack) >= 1 {
					charSpacing := ParseFloat(operandStack[0])
					textState.CharSpacing = charSpacing
					operandStack = operandStack[1:]
				} else {
					fmt.Println("Tc演算子に必要なオペランドが不足しています")
				}
			case "Tw":
				// 単語間隔の設定
				if len(operandStack) >= 1 {
					wordSpacing := ParseFloat(operandStack[0])
					textState.WordSpacing = wordSpacing
					operandStack = operandStack[1:]
				} else {
					fmt.Println("Tw演算子に必要なオペランドが不足しています")
				}
			case "Tz":
				// 水平スケーリングの設定
				if len(operandStack) >= 1 {
					horizontalScaling := ParseFloat(operandStack[0])
					textState.HorizontalScaling = horizontalScaling
					operandStack = operandStack[1:]
				} else {
					fmt.Println("Tz演算子に必要なオペランドが不足しています")
				}
			case "TL":
				// リーディングの設定
				if len(operandStack) >= 1 {
					leading := ParseFloat(operandStack[0])
					textState.Leading = leading
					operandStack = operandStack[1:]
				} else {
					fmt.Println("TL演算子に必要なオペランドが不足しています")
				}
			case "Tm":
				// テキストマトリックスの設定
				if len(operandStack) >= 6 {
					a := ParseFloat(operandStack[0])
					b := ParseFloat(operandStack[1])
					c := ParseFloat(operandStack[2])
					d := ParseFloat(operandStack[3])
					e := ParseFloat(operandStack[4])
					f := ParseFloat(operandStack[5])

					textState.Tm = Matrix{
						{a, b, 0},
						{c, d, 0},
						{e, f, 1},
					}
					textState.Tlm = textState.Tm
					operandStack = operandStack[6:]
				} else {
					fmt.Println("Tm演算子に必要なオペランドが不足しています")
				}
			case "Td":
				// テキスト位置の移動
				if len(operandStack) >= 2 {
					tx := ParseFloat(operandStack[0])
					ty := ParseFloat(operandStack[1])
					// 移動マトリックス
					m := Matrix{
						{1, 0, 0},
						{0, 1, 0},
						{tx, ty, 1},
					}
					textState.Tm = textState.Tlm.Multiply(m)
					textState.Tlm = textState.Tm
					operandStack = operandStack[2:]
				} else {
					fmt.Println("Td演算子に必要なオペランドが不足しています")
				}
			case "TD":
				// テキスト位置の移動とリーディングの設定
				if len(operandStack) >= 2 {
					tx := ParseFloat(operandStack[0])
					ty := ParseFloat(operandStack[1])
					textState.Leading = -ty
					// 移動マトリックス
					m := Matrix{
						{1, 0, 0},
						{0, 1, 0},
						{tx, ty, 1},
					}
					textState.Tm = textState.Tlm.Multiply(m)
					textState.Tlm = textState.Tm
					operandStack = operandStack[2:]
				} else {
					fmt.Println("TD演算子に必要なオペランドが不足しています")
				}
			case "T*":
				// 改行（テキストラインを Leading 分だけ下げる）
				m := Matrix{
					{1, 0, 0},
					{0, 1, 0},
					{0, -textState.Leading, 1},
				}
				textState.Tm = textState.Tlm.Multiply(m)
				textState.Tlm = textState.Tm
				operandStack = nil
			case "'":
				// 改行処理はそのまま
				m := Matrix{
					{1, 0, 0},
					{0, 1, 0},
					{0, -textState.Leading, 1},
				}
				textState.Tm = textState.Tlm.Multiply(m)
				textState.Tlm = textState.Tm
				// テキスト表示
				if len(operandStack) >= 1 {
					texts := operandStack[0] // これは"(...)"形式のPDF文字列
					operandStack = operandStack[1:]
					t := parsePDFStringToBytes(texts, to.fonts[textState.Font])
					trm := textState.Tm.Multiply(graphicsStack[len(graphicsStack)-1].CTM)
					textCommands = append(textCommands, TextCommand{
						X:        trm[2][0],
						Y:        trm[2][1],
						Z:        currentZ,
						Text:     t,
						FontID:   textState.Font,
						FontSize: textState.FontSize,
						Color:    colorState.FillColor,
					})
					currentZ++
				} else {
					fmt.Println("'演算子に必要なオペランドが不足しています")
				}

			case "\"":
				if len(operandStack) >= 3 {
					aw := ParseFloat(operandStack[0])
					ac := ParseFloat(operandStack[1])
					texts := operandStack[2] // "(...)"形式
					textState.WordSpacing = aw
					textState.CharSpacing = ac
					operandStack = operandStack[3:]
					// 改行
					m := Matrix{
						{1, 0, 0},
						{0, 1, 0},
						{0, -textState.Leading, 1},
					}
					textState.Tm = textState.Tlm.Multiply(m)
					textState.Tlm = textState.Tm
					// テキスト表示
					rawBytes := parsePDFStringToBytes(texts, to.fonts[textState.Font])
					trm := textState.Tm.Multiply(graphicsStack[len(graphicsStack)-1].CTM)
					textCommands = append(textCommands, TextCommand{
						X:        trm[2][0],
						Y:        trm[2][1],
						Z:        currentZ,
						Text:     rawBytes,
						FontID:   textState.Font,
						FontSize: textState.FontSize,
						Color:    colorState.FillColor,
					})
				} else {
					fmt.Println("\"演算子に必要なオペランドが不足しています")
				}

			// Tj演算子処理
			case "Tj":
				if len(operandStack) >= 1 {
					texts := operandStack[0] // textsは"( ... )"を含む生文字列
					operandStack = operandStack[1:]
					rawBytes := parsePDFStringToBytes(texts, to.fonts[textState.Font]) // `(` `)`を除去、\エスケープ処理した生バイト列
					trm := textState.Tm.Multiply(graphicsStack[len(graphicsStack)-1].CTM)
					scaleY := math.Sqrt(trm[1][0]*trm[1][0] + trm[1][1]*trm[1][1])

					effectiveFontSizeY := textState.FontSize * scaleY
					textCommands = append(textCommands, TextCommand{
						X:        trm[2][0],
						Y:        trm[2][1],
						Z:        currentZ,
						Text:     rawBytes,
						FontSize: effectiveFontSizeY,
						FontID:   textState.Font,
						Color:    colorState.FillColor,
					})
				} else {
					fmt.Println("Tj演算子に必要なオペランドが不足しています")
				}

			// `TJ`も同様に parsePDFStringToBytes を適用して生バイト列を抽出し、それをComputeTextPositionへ渡す

			case "TJ":
				// テキスト配列の表示
				if len(operandStack) >= 1 {
					arrayContent := operandStack[0]
					operandStack = operandStack[1:]
					textCommand := processTJ(arrayContent, textState, graphicsStack[len(graphicsStack)-1], &currentZ, to.fonts[textState.Font], *colorState)
					if textCommand != nil {
						textCommands = append(textCommands, *textCommand)
					}
				} else {
					fmt.Println("TJ演算子に必要なオペランドが不足しています")
				}
			case "Do":
				// XObjectの描画
				if len(operandStack) >= 1 {
					xObjectName := operandStack[0]
					operandStack = operandStack[1:]
					ctm := graphicsStack[len(graphicsStack)-1].CTM
					x := ctm[2][0]
					y := ctm[2][1]

					width := ctm[0][0]
					height := ctm[1][1]
					imageCommands = append(imageCommands, ImageCommand{
						X:        x,
						Y:        y,
						Z:        currentZ,
						DW:       width,
						DH:       height,
						ImageID:  strings.TrimLeft(xObjectName, "/"),
						ClipPath: pathState.Path,
					})
					currentZ++

					pathState.Path = ""
				} else {
					fmt.Println("Do演算子に必要なオペランドが不足しています")
				}
			case "m":
				// moveto: 新規パス開始点を設定
				// オペランドは x y (移動先)
				if len(operandStack) >= 2 {
					x := ParseFloat(operandStack[0])
					y := ParseFloat(operandStack[1])
					pathState.Path += fmt.Sprintf("M %f %f ", x, pageHeight-y)
					pathState.X = x
					pathState.Y = y

					operandStack = operandStack[2:]
				} else {
					fmt.Println("m演算子に必要なオペランドが不足しています")
				}

			case "l":
				// lineto: 現在のパスに直線を追加
				// オペランド: x y
				if len(operandStack) >= 2 {
					x := ParseFloat(operandStack[0])
					y := ParseFloat(operandStack[1])
					pathState.Path += fmt.Sprintf("L %f %f ", x, pageHeight-y)
					operandStack = operandStack[2:]
				} else {
					fmt.Println("l演算子に必要なオペランドが不足しています")
				}

			case "h":
				// closepath: 現在のパスを閉じる

				pathState.Path += "Z"
				operandStack = nil

			case "sc":
				// setnonstrokingcolor: 非ストローク描画色を設定
				// オペランド: カラーコンポーネント (数値が複数個)
				// DeviceGrayなら1つ、DeviceRGBなら3つ、DeviceCMYKなら4つ
				components := make([]float64, 0, len(operandStack))
				for _, op := range operandStack {
					components = append(components, ParseFloat(op))
				}
				colorState.FillColor = parseColor(components)

				operandStack = nil
			case "SC":
				// setstrokingcolor: ストローク描画色を設定
				// オペランド: カラーコンポーネント (数値が複数個)
				// DeviceGrayなら1つ、DeviceRGBなら3つ、DeviceCMYKなら4つ
				components := make([]float64, 0, len(operandStack))
				for _, op := range operandStack {
					components = append(components, ParseFloat(op))
				}
				colorState.StrokeColor = parseColor(components)
			case "cs":
				// setcolorspace: 非ストローク用カラー空間の指定
				// オペランド: カラー空間名(Nameオペランド)
				if len(operandStack) >= 1 {
					colorSpaceName := operandStack[0]
					// カラー空間設定(実装例)
					_ = colorSpaceName
					operandStack = operandStack[1:]
				} else {
					fmt.Println("cs演算子に必要なオペランドが不足しています")
				}

			case "re":
				// rectangle: 長方形パスを追加
				// オペランド: x y width height
				if len(operandStack) >= 4 {
					x := ParseFloat(operandStack[0])
					y := ParseFloat(operandStack[1])
					w := ParseFloat(operandStack[2])
					h := ParseFloat(operandStack[3])
					pathState.Path += fmt.Sprintf("M %f %f L %f %f L %f %f L %f %f ", x, pageHeight-y, x+w, pageHeight-y, x+w, pageHeight-y+h, x, pageHeight-y+h)

					operandStack = operandStack[4:]
				} else {
					fmt.Println("re演算子に必要なオペランドが不足しています")
				}

			case "W":
				// clip: 現在のパスをクリッピングパスにセット
				// オペランドなし
				// クリッピングパス設定(実装例)
				operandStack = nil

			case "n":
				// end path without fill or stroke: パスを閉じず描画せず終了
				// オペランドなし
				// パス終了(実装例)
				operandStack = nil

			case "w":
				// setlinewidth: 線幅を設定
				// オペランド: lineWidth
				if len(operandStack) >= 1 {
					lineWidth := ParseFloat(operandStack[0])
					// 線幅設定(実装例)
					_ = lineWidth
					operandStack = operandStack[1:]
				} else {
					fmt.Println("w演算子に必要なオペランドが不足しています")
				}
			case "f":
				// fill: 現在のパスを非ゼロルールで塗りつぶし
				// オペランドなし

				pathCommands = append(pathCommands, PathCommand{
					X:           pathState.X,
					Y:           pathState.Y,
					Z:           currentZ,
					Width:       pathState.Width,
					Height:      pathState.Height,
					FillColor:   colorState.FillColor,
					StrokeColor: colorState.StrokeColor,
					Path:        pathState.Path,
				})

				pathState.Path = ""

				currentZ++

				operandStack = nil

			case "S":
				// stroke: 現在のパスをストローク
				// オペランドなし

				pathCommands = append(pathCommands, PathCommand{
					X:           pathState.X,
					Y:           pathState.Y,
					Width:       pathState.Width,
					Height:      pathState.Height,
					FillColor:   colorState.FillColor,
					StrokeColor: colorState.StrokeColor,
					Path:        pathState.Path,
				})

				pathState.Path = ""

				currentZ++
				operandStack = nil

			case "f*":
				// fill (even-odd rule): 現在のパスを偶数-非偶数ルールで塗りつぶし
				// オペランドなし

				pathCommands = append(pathCommands, PathCommand{
					X:           pathState.X,
					Y:           pathState.Y,
					Z:           currentZ,
					Width:       pathState.Width,
					Height:      pathState.Height,
					FillColor:   colorState.FillColor,
					StrokeColor: colorState.StrokeColor,
					Path:        pathState.Path,
				})

				pathState.Path = ""
				currentZ++
				operandStack = nil

			case "gs":
				// set graphics state
				// オペランド: ExtGStateリソース名(例: /GS1)
				if len(operandStack) >= 1 {
					gsName := operandStack[0]
					operandStack = operandStack[1:]
					// gsNameに対応するExtGStateを取得し、CTMや透明度、ラインスタイルなどを設定する必要がある。
					// ここでは実際の処理は省略。
					_ = gsName
				} else {
					fmt.Println("gs演算子に必要なオペランドが不足しています")
				}
			case "c":
				// curveto: ベジエ曲線を現在のパスに追加
				// オペランド: x1 y1 x2 y2 x3 y3 (6つ)
				if len(operandStack) >= 6 {
					x1 := ParseFloat(operandStack[0])
					y1 := ParseFloat(operandStack[1])
					x2 := ParseFloat(operandStack[2])
					y2 := ParseFloat(operandStack[3])
					x3 := ParseFloat(operandStack[4])
					y3 := ParseFloat(operandStack[5])

					pathState.Path += fmt.Sprintf("C %f %f %f %f %f %f ", x1, pageHeight-y1, x2, pageHeight-y2, x3, pageHeight-y3)

					operandStack = operandStack[6:]
				} else {
					fmt.Println("c演算子に必要なオペランドが不足しています")
				}
			case "CS":
				// setcolorspace: ストローク用カラー空間の指定
				// オペランド: カラー空間名(Nameオペランド)
				if len(operandStack) >= 1 {
					colorSpaceName := operandStack[0]
					// カラー空間設定(実装例)
					_ = colorSpaceName
					operandStack = operandStack[1:]
				} else {
					fmt.Println("CS演算子に必要なオペランドが不足しています")
				}

			default:
				// 未知の演算子
				fmt.Printf("未知の演算子: %s\n", token.Value)
				operandStack = nil
			}
		}
	}
	return textCommands, imageCommands, pathCommands
}

func parsePDFStringToBytes(pdfString string, fonts map[byte]string) []string {
	// pdfStringは "(ABC\\)DEF)" のような形式
	// 先頭と末尾の()を削除
	if len(pdfString) < 2 {
		return []string{}
	}
	inner := pdfString[1 : len(pdfString)-1]

	var result []string
	escape := false
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if escape {
			// エスケープ後はそのまま文字を追加
			result = append(result, fonts[c])
			escape = false
		} else {
			if c == '\\' {
				escape = true
			} else {
				result = append(result, fonts[c])
			}
		}
	}
	return result
}

func (to *TokenObject) ExtractCommands(pageHeight float64) ([]TextCommand, []ImageCommand, []PathCommand) {
	tokens, err := tokenize(to.contents)
	if err != nil {
		fmt.Printf("トークンの分割に失敗しました: %v\n", err)
		return nil, nil, nil
	}

	textCommands, imageCommands, pathCommands := to.processTokens(tokens, pageHeight)
	return textCommands, imageCommands, pathCommands
}

func NewTokenObject(contents string, fonts map[string]map[byte]string) *TokenObject {
	return &TokenObject{
		fonts:    fonts,
		contents: contents,
	}
}

func parseColor(rgb []float64) string {
	r := int(rgb[0] * 255)
	g := int(rgb[1] * 255)
	b := int(rgb[2] * 255)
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}
