package pdtp

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// OffsetTable は TTF/OTF の先頭にある Offset Table (sfnt header) を表す。
type OffsetTable struct {
	SfntVersion   uint32 // 0x00010000 for TrueType
	NumTables     uint16
	SearchRange   uint16
	EntrySelector uint16
	RangeShift    uint16
}

// TableRecord はテーブルディレクトリ1つぶんを表す (タグ, チェックサム, オフセット, サイズ)
type TableRecord struct {
	Tag      uint32
	CheckSum uint32
	Offset   uint32
	Length   uint32
}

// fixOS2Table は TTF データを読み込み、OS/2 テーブルがなければ追加して返す。
func fixOS2Table(fontData []byte) ([]byte, error) {
	// 1. Offset Table のパース
	if len(fontData) < 12 {
		return nil, fmt.Errorf("input too short for offset table")
	}
	ot, err := parseOffsetTable(fontData)
	if err != nil {
		return nil, err
	}

	// 2. Table Directory のパース
	dirSize := int(ot.NumTables) * 16 // Each table record is 16 bytes
	if len(fontData) < 12+dirSize {
		return nil, fmt.Errorf("input too short for table directory")
	}
	directory, err := parseTableDirectory(fontData[12:], int(ot.NumTables))
	if err != nil {
		return nil, err
	}

	// 3. 'OS/2'テーブルがあるか確認
	hasOS2 := false
	var os2Index int
	for i, rec := range directory {
		if rec.Tag == tagStringToUint32("OS/2") {
			hasOS2 = true
			os2Index = i
			break
		}
	}

	// なければOS/2テーブルを追加
	if !hasOS2 {
		os2Index = len(directory)
		newRec := TableRecord{
			Tag:      tagStringToUint32("OS/2"),
			CheckSum: 0,
			Offset:   0, // 後で決定
			Length:   0, // 後で決定
		}
		directory = append(directory, newRec)
		ot.NumTables++
	}

	// 4. OS/2テーブルのデータを作成（最低限のサンプル）
	// ここでは version=3 (or 4など) としてダミーのフィールドを埋めています。
	// 実際にはフォントに合った値を設定するほうが望ましい。
	os2Data := buildMinimalOS2Table()

	// 5. 新しいOS/2テーブルのオフセットとサイズをディレクトリに書き込み
	directory[os2Index].Length = uint32(len(os2Data))

	// （4バイト境界合わせ用のパディングを簡易に実装: 末尾に追加すると仮定）
	alignedSize := align4(int(len(fontData)))
	newOffset := uint32(alignedSize)
	directory[os2Index].Offset = newOffset

	// 6. fontData を拡張して OS/2 テーブルを追記
	// まず 4バイト境界までパディング
	padCount := alignedSize - len(fontData)
	if padCount < 0 {
		padCount = 0
	}
	padding := make([]byte, padCount)
	fontData = append(fontData, padding...)

	// 追記
	fontData = append(fontData, os2Data...)

	// 7. テーブルのチェックサムを計算して反映
	//    Directory、headテーブルなど、すべて再計算するのが本来ですが、
	//    簡易例として、OS/2テーブルのみ計算して格納します。
	directory[os2Index].CheckSum = calcTableChecksum(fontData, int(newOffset), len(os2Data))

	// 8. Directory情報を再書き込み (numTables, ディレクトリなど)
	//    今回は簡単のため、Offset Table は書き換えずに手動でメモリ上で修正 → 再度合成
	//    searchRange, entrySelector, rangeShift なども再計算
	updateOffsetTable(&ot)
	// head.checkSumAdjustment を正しく計算するには、全テーブルの checkSum を計算 → ファイル全体の checkSum → ...
	// ここでは簡易版として割愛。必要なら下記のように実装:
	//   1) すべてのテーブル checkSum を計算
	//   2) head テーブルを読み込み checkSumAdjustment フィールドを0にして再書き込み
	//   3) ファイル全体の checkSum を計算
	//   4) checkSumAdjustment = 0xB1B0AFBA - fileChecksum
	//   5) head テーブルに再度書き込み

	// 新たなバッファに書き出して返す
	outBuf := new(bytes.Buffer)

	// Offset Table (16バイト) を書く
	if err := binary.Write(outBuf, binary.BigEndian, ot.SfntVersion); err != nil {
		return nil, err
	}
	if err := binary.Write(outBuf, binary.BigEndian, ot.NumTables); err != nil {
		return nil, err
	}
	if err := binary.Write(outBuf, binary.BigEndian, ot.SearchRange); err != nil {
		return nil, err
	}
	if err := binary.Write(outBuf, binary.BigEndian, ot.EntrySelector); err != nil {
		return nil, err
	}
	if err := binary.Write(outBuf, binary.BigEndian, ot.RangeShift); err != nil {
		return nil, err
	}

	// テーブルディレクトリ書き込み
	for _, rec := range directory {
		if err := binary.Write(outBuf, binary.BigEndian, rec.Tag); err != nil {
			return nil, err
		}
		if err := binary.Write(outBuf, binary.BigEndian, rec.CheckSum); err != nil {
			return nil, err
		}
		if err := binary.Write(outBuf, binary.BigEndian, rec.Offset); err != nil {
			return nil, err
		}
		if err := binary.Write(outBuf, binary.BigEndian, rec.Length); err != nil {
			return nil, err
		}
	}

	// ディレクトリ部分まで書き終えたオフセット
	// ここまで書いたサイズ以降がテーブル本体

	// ディレクトリで指定されたテーブルを再配置する場合は本来コピーし直す必要がありますが、
	// この例では「既存バイナリをそのまま再利用＋末尾にOS/2追加」を想定し、
	// offsetTable + directory のサイズぶんだけ読み飛ばし → 残りを付与、という簡易方針を取ります。

	// もともとのファイル先頭(Offset Table + Directory)ぶんを読み飛ばす
	oldDataPos := 12 + (int(ot.NumTables)-1)*16 // (追加前のNumTables-1)に注意
	if oldDataPos < 0 {
		oldDataPos = 12 // fallback
	}
	if oldDataPos > len(fontData) {
		oldDataPos = len(fontData)
	}
	// もとのデータのテーブル本体部分をそのまま書き込む
	outBuf.Write(fontData[oldDataPos:])

	// これでディレクトリとテーブル本体が一応1つのファイルとしてまとまる
	newData := outBuf.Bytes()

	// 最終的には head.checkSumAdjustment を再計算しないと正しいTTFとは言えませんが、
	// ここでは簡易サンプルとして終了
	return newData, nil
}

// -- 以下、サポート関数など -----------------------------------------------

// parseOffsetTable は TTF の最初の 12バイト (または16バイト) をパースする。
// ただし今回は 12バイト構造で想定(0x00010000)。
func parseOffsetTable(data []byte) (OffsetTable, error) {
	if len(data) < 12 {
		return OffsetTable{}, fmt.Errorf("data too short")
	}
	ot := OffsetTable{}
	ot.SfntVersion = binary.BigEndian.Uint32(data[0:4])
	ot.NumTables = binary.BigEndian.Uint16(data[4:6])
	ot.SearchRange = binary.BigEndian.Uint16(data[6:8])
	ot.EntrySelector = binary.BigEndian.Uint16(data[8:10])
	ot.RangeShift = binary.BigEndian.Uint16(data[10:12])
	return ot, nil
}

// parseTableDirectory はディレクトリレコードをパースする。
func parseTableDirectory(data []byte, num int) ([]TableRecord, error) {
	size := num * 16
	if len(data) < size {
		return nil, fmt.Errorf("not enough data for directory")
	}
	directory := make([]TableRecord, num)
	for i := 0; i < num; i++ {
		offset := i * 16
		dir := TableRecord{
			Tag:      binary.BigEndian.Uint32(data[offset : offset+4]),
			CheckSum: binary.BigEndian.Uint32(data[offset+4 : offset+8]),
			Offset:   binary.BigEndian.Uint32(data[offset+8 : offset+12]),
			Length:   binary.BigEndian.Uint32(data[offset+12 : offset+16]),
		}
		directory[i] = dir
	}
	return directory, nil
}

// buildMinimalOS2Table は最低限の OS/2 テーブルバイナリを作る簡易サンプル。
// ここではバージョン3 でフィールドを固定値にしている。
func buildMinimalOS2Table() []byte {
	// Version3相当(エントリ数そこそこ)だが全て0埋めでもOTSは通ることが多い。
	// ただし下記のようにいくつか最低限の値を埋める例を示す。
	// 項目数やサイズは OpenType 仕様で要確認。
	buf := new(bytes.Buffer)

	// version
	_ = binary.Write(buf, binary.BigEndian, uint16(3)) // OS/2 version = 3
	// xAvgCharWidth
	_ = binary.Write(buf, binary.BigEndian, int16(512))
	// usWeightClass (400 = normal)
	_ = binary.Write(buf, binary.BigEndian, uint16(400))
	// usWidthClass (5 = medium)
	_ = binary.Write(buf, binary.BigEndian, uint16(5))
	// fsType (0: installable embedding)
	_ = binary.Write(buf, binary.BigEndian, int16(0))
	// ySubscriptXSize
	_ = binary.Write(buf, binary.BigEndian, int16(650))
	// ySubscriptYSize
	_ = binary.Write(buf, binary.BigEndian, int16(600))
	// ySubscriptXOffset
	_ = binary.Write(buf, binary.BigEndian, int16(0))
	// ySubscriptYOffset
	_ = binary.Write(buf, binary.BigEndian, int16(75))
	// ySuperscriptXSize
	_ = binary.Write(buf, binary.BigEndian, int16(650))
	// ySuperscriptYSize
	_ = binary.Write(buf, binary.BigEndian, int16(600))
	// ySuperscriptXOffset
	_ = binary.Write(buf, binary.BigEndian, int16(0))
	// ySuperscriptYOffset
	_ = binary.Write(buf, binary.BigEndian, int16(175))
	// yStrikeoutSize
	_ = binary.Write(buf, binary.BigEndian, int16(50))
	// yStrikeoutPosition
	_ = binary.Write(buf, binary.BigEndian, int16(258))
	// sFamilyClass
	_ = binary.Write(buf, binary.BigEndian, int16(0))
	// PANOSE (10バイト)
	_ = binary.Write(buf, binary.BigEndian, [10]byte{2, 11, 6, 3, 2, 2, 0, 0, 0, 0})
	// ulUnicodeRange1 ~ 4
	for i := 0; i < 4; i++ {
		_ = binary.Write(buf, binary.BigEndian, uint32(0))
	}
	// achVendID
	_ = binary.Write(buf, binary.BigEndian, [4]byte{'G', 'o', 'F', 't'})
	// fsSelection
	_ = binary.Write(buf, binary.BigEndian, uint16(0x0040)) // REGULAR bitなど
	// usFirstCharIndex
	_ = binary.Write(buf, binary.BigEndian, uint16(32))
	// usLastCharIndex
	_ = binary.Write(buf, binary.BigEndian, uint16(65535))
	// sTypoAscender
	_ = binary.Write(buf, binary.BigEndian, int16(800))
	// sTypoDescender
	_ = binary.Write(buf, binary.BigEndian, int16(-200))
	// sTypoLineGap
	_ = binary.Write(buf, binary.BigEndian, int16(75))
	// usWinAscent
	_ = binary.Write(buf, binary.BigEndian, uint16(900))
	// usWinDescent
	_ = binary.Write(buf, binary.BigEndian, uint16(250))
	// ulCodePageRange1,2
	for i := 0; i < 2; i++ {
		_ = binary.Write(buf, binary.BigEndian, uint32(0))
	}
	// 以下は version2以降などで追加のフィールドが続く可能性あり
	// version3,4 だとさらにフィールド有（sxHeight, sCapHeight 等）。
	// サンプルのため省略。

	return buf.Bytes()
}

// calcTableChecksum は TTF 仕様におけるテーブルの checksum を計算する簡易関数。
// 4バイト単位で加算する。実際にはバイトが揃わない場合の処理等必要だが簡易化。
func calcTableChecksum(data []byte, offset int, length int) uint32 {
	var sum uint32
	end := offset + length
	// end が data のサイズを超えていたら、そもそも不正。ひとまず clampする or エラーを返す
	if end > len(data) {
		end = len(data)
	}

	for i := offset; i < end; i += 4 {
		// 残りが4バイト未満の場合は0埋めして読み取る
		if i+4 <= end {
			val := binary.BigEndian.Uint32(data[i : i+4])
			sum += val
		} else {
			// 足りないぶんは0埋め
			tmp := make([]byte, 4)
			copy(tmp, data[i:end])
			val := binary.BigEndian.Uint32(tmp)
			sum += val
		}
	}
	return sum
}

// updateOffsetTable は numTables を元に searchRange, entrySelector, rangeShift を再計算 (簡易版).
func updateOffsetTable(ot *OffsetTable) {
	// TrueTypeでよく使われる計算式
	//   searchRange = 最大の power of 2 * 16 <= numTables * 16
	//   entrySelector = log2(最大のpower of 2)
	//   rangeShift = numTables*16 - searchRange
	num := int(ot.NumTables)
	pow2 := 1
	shift := 0
	for (pow2 << 1) <= num {
		pow2 <<= 1
		shift++
	}
	ot.SearchRange = uint16(pow2 * 16)
	ot.EntrySelector = uint16(shift)
	ot.RangeShift = uint16(num*16) - ot.SearchRange
}

// align4 は int値を4バイト境界に揃える (上向きに切り上げ)
func align4(n int) int {
	if n%4 == 0 {
		return n
	}
	return n + (4 - n%4)
}

// tagStringToUint32 は 'OS/2' など4文字を uint32 に変換 (ビッグエンディアン)
func tagStringToUint32(s string) uint32 {
	if len(s) != 4 {
		panic("tag must be 4 chars")
	}
	return (uint32(s[0]) << 24) | (uint32(s[1]) << 16) | (uint32(s[2]) << 8) | uint32(s[3])
}

// -----------------------------------------------------

// func main() {
// 	// 例: フォントファイルを読み込み、OS/2 テーブルが無ければ追加して書き出す
// 	if len(os.Args) < 3 {
// 		fmt.Println("Usage: fixfont <in.ttf> <out.ttf>")
// 		return
// 	}
// 	inFile := os.Args[1]
// 	outFile := os.Args[2]

// 	raw, err := os.ReadFile(inFile)
// 	if err != nil {
// 		fmt.Println("read error:", err)
// 		return
// 	}
// 	newData, err := fixOS2Table(raw)
// 	if err != nil {
// 		fmt.Println("fix error:", err)
// 		return
// 	}

// 	err = os.WriteFile(outFile, newData, 0644)
// 	if err != nil {
// 		fmt.Println("write error:", err)
// 		return
// 	}
// 	fmt.Println("Done:", outFile)
// }
