package pdtp

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
)

type Font struct {
	FontID      string
	FontDataRef PDFRef
	fontMap     map[byte]string
}

func (f *Font) ToUnicode(b byte) string {
	return f.fontMap[b]
}

type XRefTableElement struct {
	ObjNum     PDFRef
	GenNum     PDFRef
	offsetByte int64
}

type PDFRef int64

type Catalog struct {
	PagesRef PDFRef
}

type PageTree struct {
	Count      int
	Kids       []PDFRef
	PageWidth  float64
	PageHeight float64
}

type Page struct {
	ContentsRef  PDFRef
	ResourcesRef PDFRef
	PageWidth    float64
	PageHeight   float64
}

type ExtractedImage struct {
	Data     []byte
	MaskData []byte
	Width    float64
	Height   float64
	Ext      string
}

type IPDFParser interface {
	StreamPageContents(pageNum int, outCh chan<- ParsedData) error
	GetCatalog() (*Catalog, error)
	GetObject(ref PDFRef) (PDFObject, error)
	GetPageByNumber(pageNum int) (*Page, error)

	Close() error
}

// PDFParser は PDFParser の基本実装例
// (実際のPDF解析の詳細は TODO)
type PDFParser struct {
	file      *os.File
	xrefTable map[PDFRef]XRefTableElement
	root      PDFRef
	pageQueue []Page
	fonts     map[string]Font
}

func NewPDFParser(pathname string) (*PDFParser, error) {
	// FIXME: ファイルを開く場合クライアントサイドでのファイルアクセスを考慮して、特定のディレクトリ以下のファイルのみを許可する
	file, err := os.Open(pathname)
	if err != nil {
		return nil, err
	}
	xrefTable, rootMetadata, err := parseXrefTable(file)
	if err != nil {
		return nil, err
	}
	rootObject, err := parseMetadata(*rootMetadata)
	if err != nil {
		return nil, err
	}
	rootString, found := findTarget(rootObject, "Root")
	if !found {
		return nil, errors.New("root not found")
	}
	root, ok := rootString.(string)
	if !ok {
		return nil, errors.New("root is not string")
	}
	rootRefs := strings.Split(root, " ")
	if len(rootRefs) != 3 {
		return nil, errors.New("root format error")
	}
	rootObjNum, err := strconv.Atoi(rootRefs[0])
	if err != nil {
		return nil, err
	}

	rootRef := xrefTable[PDFRef(rootObjNum)].ObjNum

	return &PDFParser{file: file, xrefTable: xrefTable, root: rootRef, pageQueue: nil, fonts: make(map[string]Font)}, nil
}

func (p *PDFParser) ParseObject(ref PDFRef) (PDFObject, error) {
	object := p.xrefTable[ref]
	return parseMetadata(loadObject(p.file, object.offsetByte))
}

func loadObject(file *os.File, offsetByte int64) string {
	file.Seek(int64(offsetByte), io.SeekStart)
	scanner := bufio.NewScanner(file)
	buffer := ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "endobj") || strings.HasPrefix(line, "stream") {
			break
		}

		buffer += line + "\n"
	}
	buffer = strings.Split(buffer, "obj")[1]
	return buffer
}

type ImageRefCommand struct {
	X        float64 // X座標
	Y        float64 // Y座標
	Z        int64   // Z座標
	DW       float64 // 表示横幅
	DH       float64 // 表示縦幅
	ImageRef PDFRef  // 画像ID
	Page     int64
}

// StreamPageContents は 指定ページからデータを解析し、チャネルへ送る
func (p *PDFParser) StreamPageContents(ctx context.Context, pageNum int64, insertData func(data ParsedData)) error {
	c, err := p.GetCatalog()
	if err != nil {
		return err
	}
	err = p.loadPageObject(*c)
	if err != nil {
		return err
	}

	sequence := generateSequence(int64(len(p.pageQueue)), int64(pageNum))
	// FIXME:capacityが0であるため追加するたびにメモリ再割り当てが発生している
	imgCommands := make([]ImageRefCommand, 0)
	fontFileList := make(map[string]PDFRef, 0)
	for _, i := range sequence {
		page, err := p.ExtractPage(int(i))
		if err != nil {
			return err
		}
		insertData(&ParsedPage{
			Width:  page.PageWidth,
			Height: page.PageHeight,
			Page:   int64(i),
		})
		err = p.ExtractFont(page.ResourcesRef)
		if err != nil {
			return err
		}
		tc, ic, err := p.ExtractPageContents(page.ContentsRef)
		if err != nil {
			return err
		}
		for _, cmd := range tc {
			texts := ""
			for _, b := range cmd.Text {
				texts += b
			}
			insertData(&ParsedText{
				X:        cmd.X,
				Y:        cmd.Y,
				Z:        cmd.Z,
				Text:     texts,
				FontID:   cmd.FontID,
				FontSize: cmd.FontSize,
				Page:     int64(i),
			})
			fontFileList[cmd.FontID] = p.fonts[cmd.FontID].FontDataRef
		}
		imgs, err := p.ExtractImageRefs(page.ResourcesRef)
		if err != nil {
			log.Println(err)
		}
		for _, cmd := range ic {
			ir := PDFRef(imgs[cmd.ImageID])
			if ir == 0 {
				return errors.New(fmt.Sprintf("Image not found: %d", cmd.ImageID))
			}

			c := ImageRefCommand{
				X:        cmd.X,
				Y:        cmd.Y,
				Z:        cmd.Z,
				DW:       cmd.DW,
				DH:       cmd.DH,
				ImageRef: ir,
				Page:     int64(i),
			}

			imgCommands = append(imgCommands, c)
		}

	}

	for _, cmd := range imgCommands {
		img, err := p.ExtractImageStream(cmd.ImageRef)
		if err != nil {
			log.Println("Failed to extract image stream: %v", err)
			return err
		}

		insertData(&ParsedImage{
			X:        cmd.X,
			Y:        cmd.Y,
			Z:        cmd.Z,
			Width:    img.Width,
			Height:   img.Height,
			DW:       cmd.DW,
			DH:       cmd.DH,
			Data:     img.Data,
			MaskData: img.MaskData,
			Page:     cmd.Page,
			Ext:      img.Ext,
		})

	}

	for key, font := range fontFileList {
		fontStream := p.ExtractFontStream(font)
		insertData(&ParsedFont{
			FontID: key,
			Data:   []byte(fontStream),
		})
	}
	return nil
}

func (p *PDFParser) GetMediaBox(page PDFObject) ([]int, error) {
	mediaBox, found := findTarget(page, "MediaBox")
	if found {
		mediaBoxArray, ok := mediaBox.([]PDFObject)
		if !ok {
			return nil, errors.New("MediaBox is not array")
		}
		intMediaBox := make([]int, 0)
		for _, v := range mediaBoxArray {
			intV, ok := v.(int)
			if !ok {
				return nil, errors.New("MediaBox is not int")
			}
			intMediaBox = append(intMediaBox, intV)
		}
		return intMediaBox, nil
	} else {
		parentRef, found := findTargetRef(page, "Parent")
		if !found {
			return nil, errors.New("mediaBox not found")
		}
		parent, err := p.ParseObject(parentRef)
		if err != nil {
			return nil, err
		}
		return p.GetMediaBox(parent)
	}
}

func generateSequence(length int64, current int64) []int64 {
	// Create a slice to hold all integers from 0 to length-1
	numbers := make([]int64, length)
	for i := int64(0); i < length; i++ {
		numbers[i] = i + 1
	}

	// Sort the numbers based on their distance from 'current'
	sort.Slice(numbers, func(i, j int) bool {
		distanceI := math.Abs(float64(numbers[i] - current))
		distanceJ := math.Abs(float64(numbers[j] - current))
		if distanceI == distanceJ {
			return numbers[i] < numbers[j] // Smaller number comes first
		}
		return distanceI < distanceJ // Sort by distance
	})

	return numbers
}

func (p *PDFParser) GetCatalog() (*Catalog, error) {
	root, err := p.ParseObject(p.root)
	if err != nil {
		return nil, err
	}
	pagesRef, found := findTargetRef(root, "Pages")
	if !found {
		return nil, errors.New("Pages not found")
	}
	return &Catalog{pagesRef}, nil
}

func (p *PDFParser) loadPageObject(catalogRef Catalog) error {
	pages, err := p.ParseObject(catalogRef.PagesRef)
	if err != nil {
		return err
	}
	kids, found := findTargetRefs(pages, "Kids")
	if !found {
		return errors.New("kids not found ")
	}
	for _, kid := range kids {
		err = p.loadPerPageObject(kid)
		if err != nil {
			return err
		}
	}
	return nil

}

func (p *PDFParser) loadPerPageObject(ptRef PDFRef) error {
	pt, err := p.ParseObject(ptRef)
	if err != nil {
		return err
	}
	t, found := findTarget(pt, "Type")
	if !found {
		return errors.New("Type not found")
	}
	if t == "Pages" {
		kids, found := findTargetRefs(pt, "Kids")
		if !found {
			return errors.New("Kids not found")
		}

		for _, kid := range kids {
			err := p.loadPerPageObject(kid)
			if err != nil {
				return err
			}
		}
	} else if t == "Page" {
		contentsRef, found := findTargetRef(pt, "Contents")
		if !found {
			return errors.New("Contents not found")
		}

		resourcesRef, found := findTargetRef(pt, "Resources")
		if !found {
			return errors.New("Resources not found")
		}

		intMediaBox, err := p.GetMediaBox(pt)
		if err != nil {
			return err
		}

		pageWidth := intMediaBox[2] - intMediaBox[0]
		pageHeight := intMediaBox[3] - intMediaBox[1]
		p.pageQueue = append(p.pageQueue, Page{contentsRef, resourcesRef, float64(pageWidth), float64(pageHeight)})
	} else {
		return errors.New(fmt.Sprintf("Type is not Pages or Page: %s", t))
	}
	return nil
}

// Close は ファイルやリソースを解放 (TODO)
func (p *PDFParser) Close() error {
	// ファイルを閉じる
	if err := p.file.Close(); err != nil {
		return err
	}

	return nil
}

func (p *PDFParser) ExtractPage(pageNum int) (*Page, error) {
	if len(p.pageQueue) == 0 {
		return nil, errors.New("no page")
	}
	if len(p.pageQueue) < pageNum {
		return nil, errors.New("index out of range page")
	}
	page := p.pageQueue[pageNum-1]
	return &page, nil
}
func (p *PDFParser) ExtractPageContents(contentsRef PDFRef) ([]TextCommand, []ImageCommand, error) {
	contents, err := p.ParseObject(contentsRef)
	if err != nil {
		return nil, nil, err
	}
	filter, found := findTarget(contents, "Filter")

	contentsStream := p.ExtractStreamByRef(contentsRef)
	if found && filter == "FlateDecode" {
		contentsStream = deCompressStream(contentsStream)
	}
	fontMap := make(map[string]map[byte]string)
	for _, font := range p.fonts {
		fontMap[font.FontID] = font.fontMap
	}
	to := NewTokenObject(string(contentsStream), fontMap)
	tc, ic := to.ExtractCommands()
	return tc, ic, nil
}

func (p *PDFParser) ExtractFont(resourceRef PDFRef) error {
	resources, err := p.ParseObject(resourceRef)
	if err != nil {
		return err
	}
	fonts, found := findTarget(resources, "Font")
	if !found {
		return errors.New("Font not found")
	}
	fontsMap, ok := fonts.(map[string]PDFObject)
	if !ok {
		return errors.New("Font is not map")
	}
	for key, value := range fontsMap {
		fontRef, ok := parseRef(value.(string))
		if !ok {
			return errors.New("Font format error")
		}
		font, err := p.ParseObject(fontRef)
		if err != nil {
			return err
		}
		subType, found := findTarget(font, "Subtype")
		if !found {
			return errors.New("Subtype not found")
		}

		if subType == "TrueType" {
			toUnicodeRef, found := findTargetRef(font, "ToUnicode")
			if !found {
				return errors.New("ToUnicode not found")
			}
			toUnicode, err := p.ParseObject(toUnicodeRef)
			if err != nil {
				return err
			}
			filter, found := findTarget(toUnicode, "Filter")

			toUnicodeStream := p.ExtractStreamByRef(toUnicodeRef)
			if found && filter == "FlateDecode" {
				toUnicodeStream = deCompressStream(toUnicodeStream)
			}
			firstChar, found := findTarget(font, "FirstChar")
			if !found {
				return errors.New("FirstChar not found")
			}
			firstCharInt, ok := firstChar.(int)
			if !ok {
				return errors.New("FirstChar is not int")
			}
			cmaps, err := p.ExtractCMaps(string(toUnicodeStream), int8(firstCharInt))
			if err != nil {
				return err
			}
			fontFileRef := PDFRef(0)
			FontDescriptorRef, found := findTargetRef(font, "FontDescriptor")
			if found {
				FontDescriptor, err := p.ParseObject(FontDescriptorRef)
				if err != nil {
					return err
				}
				fontFileRef, found = findTargetRef(FontDescriptor, "FontFile2")
				if !found {
					return errors.New("FontFile not found")
				}
			}
			p.fonts[key] = Font{key, fontFileRef, cmaps}
		} else if subType == "Type0" {
			// descendantFontRefs, found := findTargetRefs(font, "DescendantFonts")
			// if !found {
			// 	return nil, errors.New("DescendantFonts not found")
			// }

		}
	}
	return nil
}

func (p *PDFParser) ExtractImageRefs(resourceRef PDFRef) (map[string]PDFRef, error) {
	images := make(map[string]PDFRef, 0)
	resources, err := p.ParseObject(resourceRef)
	if err != nil {
		return nil, err
	}
	XObjects, found := findTarget(resources, "XObject")
	if !found {
		return nil, nil
	}
	imagesMap, ok := XObjects.(map[string]PDFObject)

	if !ok {
		return nil, errors.New("XObject is not map")
	}

	for key, value := range imagesMap {
		imageRef, ok := parseRef(value.(string))
		if !ok {
			return nil, errors.New("Image format error")
		}
		images[key] = imageRef
	}
	return images, nil
}

func (p *PDFParser) ExtractImageStream(imageRef PDFRef) (*ExtractedImage, error) {
	image, err := p.ParseObject(imageRef)
	if err != nil {
		return nil, err
	}
	imageStream := p.ExtractStreamByRef(imageRef)
	imageFilter, found := findTarget(image, "Filter")
	if !found {
		return nil, errors.New("image Filter not found")
	}
	smask, found := findTarget(image, "SMask")
	smaskStream := make([]byte, 0)
	if found {

		smaskRef, ok := parseRef(smask.(string))
		if !ok {
			return nil, errors.New("SMask format error")
		}

		smaskStream = p.ExtractStreamByRef(smaskRef)
	}
	var Ext string

	if imageFilter == "DCTDecode" {
		Ext = "jpg"
	} else {
		Ext = "png"
	}
	Width, found := findTarget(image, "Width")
	Height, found := findTarget(image, "Height")
	if !found {
		return nil, errors.New("Width or Height not found")
	}
	WidthInt, ok := Width.(int)
	HeightInt, ok := Height.(int)
	if !ok {
		return nil, errors.New("Width or Height is not int")
	}
	WidthFloat := float64(WidthInt)
	HeightFloat := float64(HeightInt)
	return &ExtractedImage{
		Data:     (imageStream),
		MaskData: (smaskStream),
		Width:    WidthFloat,
		Height:   HeightFloat,
		Ext:      Ext,
	}, nil

}
func (p *PDFParser) ExtractCMaps(cmapsString string, firstCharNumber int8) (map[byte]string, error) {
	re := regexp.MustCompile(`(?s)\d+\s+beginbfrange\s+(.*?)\s+endbfrange`)

	matches := re.FindAllStringSubmatch(cmapsString, -1)

	var substrings string

	for _, match := range matches {
		substrings = substrings + "\n" + match[1]
	}
	values := make(map[byte]string)
	cmaps := strings.Split(substrings, "\n")
	cnt := int8(0)
	for _, cmap := range cmaps {
		if cmap == "" {
			continue
		}
		split := strings.Split(strings.Trim(strings.Trim(cmap, "<"), ">"), "><")

		startIndex, err := strconv.ParseInt(split[0], 16, 64)
		if err != nil {
			return nil, err
		}

		endIndex, err := strconv.ParseInt(split[1], 16, 64)
		if err != nil {
			return nil, err
		}
		value, err := strconv.ParseInt(split[2], 16, 64)

		if err != nil {
			return nil, err
		}

		for i := 0; i <= int(endIndex-startIndex); i++ {
			values[uint8(firstCharNumber+cnt)] = string(int(value) + int(i))
			cnt += 1
		}
	}
	return values, nil

}

func (p *PDFParser) ExtractFontStream(fontRef PDFRef) []byte {
	font, err := p.ParseObject(fontRef)
	if err != nil {
		log.Fatalf("Failed to parse font object: %v", err)
	}
	fontStream := p.ExtractStreamByRef(fontRef)
	fontFilter, found := findTarget(font, "Filter")
	if !found {
		return fontStream
	}
	if fontFilter == "FlateDecode" {
		fontStream = deCompressStream(fontStream)
	}
	fontLength1, found := findTarget(font, "Length1")
	if found {
		fontLength1Int, ok := fontLength1.(int)
		if !ok {
			log.Println(ErrParserParseObjectError)
			return nil
		}
		fontStream = fontStream[:fontLength1Int]
	}
	return fontStream
}

func (p *PDFParser) ExtractStreamByRef(ref PDFRef) []byte {
	objectString := loadObject(p.file, p.xrefTable[ref].offsetByte)
	object, err := parseMetadata(objectString)
	if err != nil {
		log.Println(ErrParserParseObjectError)
		return nil
	}
	length, found := findTarget(object, "Length")
	if !found {
		// FIXME: エラーハンドリングを考える
		return nil
	}
	lengthInt, ok := length.(int)
	if !ok {
		log.Println(ErrParserParseObjectError)
		return nil
	}
	totalOffset := int64(len(fmt.Sprintf("%v 0 obj", ref))) + p.xrefTable[ref].offsetByte + int64(len(objectString)) + int64(len("stream\n"))
	buffer := make([]byte, lengthInt)
	p.file.Seek(totalOffset, io.SeekStart)
	_, err = p.file.Read(buffer)
	if err != nil {
		log.Println(ErrParserReadStreamError)
	}

	return buffer

}

func deCompressStream(buffer []byte) []byte {
	fr, err := zlib.NewReader(bytes.NewReader(buffer))
	if err != nil {
		log.Println(string(debug.Stack()))
		log.Println(ErrParserDeCompressionError)
	}

	defer fr.Close()

	var decompressedData bytes.Buffer
	_, err = io.Copy(&decompressedData, fr)
	if err != nil {
		log.Println(string(debug.Stack()))
		log.Println("Failed to decompress data: %v", err)
	}
	return decompressedData.Bytes()
}

func parseXrefTable(file *os.File) (map[PDFRef]XRefTableElement, *string, error) {
	xrefTableOffsetByte := getXrefTableOffsetByte(file)
	if xrefTableOffsetByte == nil {
		return nil, nil, errors.New("xref table offset not found")
	}
	file.Seek(int64(*xrefTableOffsetByte), io.SeekStart)

	scanner := bufio.NewScanner(file)
	scanner.Scan()
	line := scanner.Text()
	if line != "xref" {
		return nil, nil, errors.New("xref table not found")
	}
	scanner.Scan()
	line = scanner.Text()

	lns := strings.Split(line, " ")
	if len(lns) != 2 {
		return nil, nil, errors.New("xref table format error")
	}
	ln := lns[1]
	lnNum, err := strconv.Atoi(ln)
	if err != nil {
		return nil, nil, err
	}
	xrefTable := make(map[PDFRef]XRefTableElement, lnNum)
	cnt := PDFRef(0)
	for i := 0; i < lnNum; i++ {
		scanner.Scan()
		line = scanner.Text()
		if line == "trailer" {
			break
		}
		lns = strings.Split(strings.TrimSpace(line), " ")
		if len(lns) != 3 {
			return nil, nil, errors.New("xref table line format error")
		}

		genNum, err := strconv.Atoi(lns[1])
		if err != nil {
			return nil, nil, err
		}
		offsetByte, err := strconv.Atoi(lns[0])
		if err != nil {
			return nil, nil, err
		}
		xrefTable[cnt] = XRefTableElement{PDFRef(cnt), PDFRef(genNum), int64(offsetByte)}
		cnt++
	}

	rootObject := ""
	for scanner.Scan() {
		line = scanner.Text()
		if strings.Contains(line, "trailer") {
			continue
		}
		rootObject += line
		if strings.Contains(line, ">>") {
			break
		}
	}

	return xrefTable, &rootObject, nil

}

func getXrefTableOffsetByte(file *os.File) *int {
	file.Seek(-100, io.SeekEnd)
	scanner := bufio.NewScanner(file)
	nextIsXRef := false
	b := int(0)
	includeEOF := false
	for scanner.Scan() {
		line := scanner.Text()
		if nextIsXRef {
			intBytes, err := strconv.Atoi(line)
			if err != nil {
				panic(err)
			}
			b = intBytes
			nextIsXRef = false
		}
		if line == "startxref" {
			nextIsXRef = true

		}
		if line == "%%EOF" {
			includeEOF = true
		}
	}
	if includeEOF {
		return &b
	}
	return nil
}
