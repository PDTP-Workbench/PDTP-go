package pdtp

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	// "log" // Removed standard log
	"log/slog"
	"regexp"
	// "runtime/debug" // Removed unless specifically needed for a new reason
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

type IPDFFile interface {
	io.Reader
	io.Closer
	io.Seeker
}

type ReadSeekCloser interface {
	io.ReadCloser
	io.Seeker
}
type SeekerCloser struct {
	io.ReadCloser
	io.Seeker
}

type PDFFile struct {
	reader   ReadSeekCloser
	original io.Closer
}

func (f *PDFFile) Close() error {
	if f.original != nil {
		return f.original.Close()
	}
	return f.reader.Close()
}

func (f *PDFFile) Read(p []byte) (int, error) {
	return f.reader.Read(p)
}

func (f *PDFFile) Seek(offset int64, whence int) (int64, error) {
	return f.reader.Seek(offset, whence)
}

func NewPDFFile(rc io.ReadCloser) (IPDFFile, error) {
	if seeker, ok := rc.(io.Seeker); ok {
		return &PDFFile{
			reader: SeekerCloser{ReadCloser: rc, Seeker: seeker},
		}, nil
	}

	data, err := io.ReadAll(rc)
	if err != nil {
		rc.Close()
		return nil, fmt.Errorf("failed to read data for seeking: %w", err)
	}
	rc.Close()

	reader := bytes.NewReader(data)
	return &PDFFile{
		reader:   SeekerCloser{ReadCloser: io.NopCloser(reader), Seeker: reader},
		original: nil,
	}, nil
}

type PDFParser struct {
	file      IPDFFile
	xrefTable map[PDFRef]XRefTableElement
	root      PDFRef
	pageQueue []Page
	fonts     map[string]Font
	logger    *slog.Logger
}

func NewPDFParser(open func() (IPDFFile, error), logger *slog.Logger) (*PDFParser, error) {
	if logger == nil {
		logger = slog.Default()
	}
	file, err := open()
	if err != nil {
		// Logged by the caller of NewPDFParser
		return nil, fmt.Errorf("failed to open PDF file: %w", err)
	}

	xrefTable, rootMetadata, err := parseXrefTable(file, logger) // Pass logger
	if err != nil {
		logger.Error("Failed to parse XRef table", "error", err)
		return nil, fmt.Errorf("failed to parse XRef table: %w", err)
	}

	if rootMetadata == nil || *rootMetadata == "" {
		logger.Error("XRef table parsing returned empty root metadata")
		return nil, errors.New("empty root metadata from XRef table parsing")
	}

	rootObject, err := parseMetadata(*rootMetadata)
	if err != nil {
		logger.Error("Failed to parse root metadata", "error", err, "metadata_string", *rootMetadata)
		return nil, fmt.Errorf("failed to parse root metadata: %w", err)
	}

	rootString, found := findTarget(rootObject, "Root")
	if !found {
		logger.Error("Root entry not found in PDF root object", "root_object", rootObject)
		return nil, errors.New("root entry not found in PDF root object")
	}
	rootStrVal, ok := rootString.(string)
	if !ok {
		logger.Error("Root entry in PDF root object is not a string reference", "type", fmt.Sprintf("%T", rootString))
		return nil, fmt.Errorf("root entry is not a string: got %T", rootString)
	}

	rootRefs := strings.Split(rootStrVal, " ")
	if len(rootRefs) != 3 {
		logger.Error("Root reference format error", "root_string", rootStrVal)
		return nil, fmt.Errorf("root reference format error: %s", rootStrVal)
	}
	rootObjNum, err := strconv.Atoi(rootRefs[0])
	if err != nil {
		logger.Error("Failed to parse root object number", "error", err, "root_refs_part", rootRefs[0])
		return nil, fmt.Errorf("failed to parse root object number '%s': %w", rootRefs[0], err)
	}

	// Ensure the root object number exists in the xref table
	rootRefElement, موجود := xrefTable[PDFRef(rootObjNum)]
	if !موجود {
		logger.Error("Root object number from trailer not found in XRef table", "root_obj_num", rootObjNum)
		return nil, fmt.Errorf("root object %d not found in XRef table", rootObjNum)
	}
	rootRef := rootRefElement.ObjNum

	return &PDFParser{file: file, xrefTable: xrefTable, root: rootRef, pageQueue: nil, fonts: make(map[string]Font), logger: logger}, nil
}

func (p *PDFParser) ParseObject(ref PDFRef) (PDFObject, error) {
	objectInfo, ok := p.xrefTable[ref]
	if !ok {
		err := fmt.Errorf("object ref %d not found in xref table", ref)
		p.logger.Error("Error parsing object: ref not found in xref", "ref", ref)
		return nil, err
	}
	objectString, err := loadObject(p.file, objectInfo.offsetByte)
	if err != nil {
		p.logger.Error("Error loading object content", "ref", ref, "offset", objectInfo.offsetByte, "error", err)
		return nil, fmt.Errorf("failed to load object %d: %w", ref, err)
	}
	parsedObject, err := parseMetadata(objectString)
	if err != nil {
		p.logger.Error("Error parsing metadata for object", "ref", ref, "object_string_snippet", firstN(objectString, 100), "error", err)
		if strings.TrimSpace(objectString) == "" {
			p.logger.Warn("Attempted to parse metadata from empty object string", "ref", ref)
		}
		return nil, fmt.Errorf("failed to parse metadata for object %d: %w", ref, err)
	}
	return parsedObject, nil
}

func loadObject(file IPDFFile, offsetByte int64) (string, error) {
	if _, err := file.Seek(int64(offsetByte), io.SeekStart); err != nil {
		return "", fmt.Errorf("failed to seek to object offset %d: %w", offsetByte, err)
	}
	scanner := bufio.NewScanner(file)
	var buffer strings.Builder
	var objDefLineSkipped bool = false

	for scanner.Scan() {
		line := scanner.Text()
		if !objDefLineSkipped {
			if strings.Contains(line, " obj") {
				objDefLineSkipped = true
				parts := strings.SplitN(line, "obj", 2)
				if len(parts) > 1 {
					trimmedPart := strings.TrimSpace(parts[1])
					if trimmedPart != "" {
						buffer.WriteString(trimmedPart)
						buffer.WriteString("\n")
					}
				}
				continue
			}
			// If first line is not "X Y obj", assume it's part of content or malformed.
			// For robustness, we'll assume content might start immediately or after obj on a later line.
			// However, standard PDFs have "X Y obj" clearly.
		}

		if strings.HasPrefix(line, "endobj") || strings.HasPrefix(line, "stream") {
			break
		}
		buffer.WriteString(line)
		buffer.WriteString("\n")
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error scanning PDF object at offset %d: %w", offsetByte, err)
	}
	return strings.TrimSpace(buffer.String()), nil
}

type ImageRefCommand struct {
	X        float64
	Y        float64
	Z        int64
	DW       float64
	DH       float64
	ImageRef PDFRef
	Page     int64
	ClipPath string
}

func (p *PDFParser) StreamPageContents(ctx context.Context, start, end, base int64, insertData func(data ParsedData)) error {
	c, err := p.GetCatalog()
	if err != nil {
		p.logger.Error("Failed to get catalog", "error", err)
		return fmt.Errorf("failed to get catalog: %w", err)
	}
	err = p.loadPageObject(*c)
	if err != nil {
		p.logger.Error("Failed to load page objects", "error", err)
		return fmt.Errorf("failed to load page objects: %w", err)
	}
	start, end, base = normalizePageNum(start, end, base, int64(len(p.pageQueue)))
	sequence, err := generateSequence(start, end, base)
	if err != nil {
		p.logger.Error("Failed to generate page sequence", "error", err)
		return fmt.Errorf("failed to generate page sequence: %w", err)
	}

	imgCommands := make([]ImageRefCommand, 0)
	fontFileList := make(map[string]PDFRef)
	for _, i := range sequence {
		page, err := p.ExtractPage(int(i))
		if err != nil {
			p.logger.Warn("Failed to extract page", "page_num", i, "error", err)
			return fmt.Errorf("failed to extract page %d: %w", i, err)
		}
		insertData(&ParsedPage{
			Width:  page.PageWidth,
			Height: page.PageHeight,
			Page:   int64(i),
		})
		err = p.ExtractFont(page.ResourcesRef)
		if err != nil {
			p.logger.Warn("Failed to extract font for page", "page_num", i, "resources_ref", page.ResourcesRef, "error", err)
			// Continue if font extraction fails for a page? Or return error?
			// For now, returning error to be safe.
			return fmt.Errorf("failed to extract font for page %d: %w", i, err)
		}
		tc, ic, pc, err := p.ExtractPageContents(page.ContentsRef, page.PageHeight)
		if err != nil {
			p.logger.Warn("Failed to extract page contents", "page_num", i, "contents_ref", page.ContentsRef, "error", err)
			return fmt.Errorf("failed to extract page contents for page %d: %w", i, err)
		}
		for _, cmd := range tc {
			texts := ""
			for _, b := range cmd.Text {
				texts += b
			}
			insertData(&ParsedText{
				X: cmd.X, Y: cmd.Y, Z: cmd.Z, Text: texts, FontID: cmd.FontID, FontSize: cmd.FontSize, Page: int64(i), Color: cmd.Color,
			})
			fontFileList[cmd.FontID] = p.fonts[cmd.FontID].FontDataRef
		}
		for _, cmd := range pc {
			insertData(&ParsedPath{
				X: cmd.X, Y: cmd.Y, Z: cmd.Z, Width: cmd.Width, Height: cmd.Height, Page: int64(i), Path: cmd.Path, StrokeColor: cmd.StrokeColor, FillColor: cmd.FillColor,
			})
		}
		imgs, err := p.ExtractImageRefs(page.ResourcesRef)
		if err != nil {
			p.logger.Warn("Failed to extract image refs for page", "page_num", i, "resources_ref", page.ResourcesRef, "error", err)
			// Non-fatal for image refs?
		}
		for _, cmd := range ic {
			ir, ok := imgs[cmd.ImageID]
			if !ok || ir == 0 { // Check ok for safety, though 0 is often used for missing.
				p.logger.Warn("Image ID from content stream not found in page resources", "image_id", cmd.ImageID, "page_num", i)
				// Skip this image command or return error? For now, skip.
				continue
			}
			imgCommands = append(imgCommands, ImageRefCommand{
				X: cmd.X, Y: cmd.Y, Z: cmd.Z, DW: cmd.DW, DH: cmd.DH, ImageRef: ir, Page: int64(i), ClipPath: cmd.ClipPath,
			})
		}
	}

	for _, cmd := range imgCommands {
		img, err := p.ExtractImageStream(cmd.ImageRef)
		if err != nil {
			p.logger.Warn("Failed to extract image stream from command", "image_ref", cmd.ImageRef, "page_num", cmd.Page, "error", err)
			// Skip this image if extraction fails
			continue
		}
		insertData(&ParsedImage{
			X: cmd.X, Y: cmd.Y, Z: cmd.Z, Width: img.Width, Height: img.Height, DW: cmd.DW, DH: cmd.DH, Data: img.Data, MaskData: img.MaskData, Page: cmd.Page, Ext: img.Ext, ClipPath: cmd.ClipPath,
		})
	}

	for key, fontRef := range fontFileList {
		fontStreamBytes, err := p.ExtractFontStream(fontRef)
		if err != nil {
			p.logger.Warn("Failed to extract font stream for listed font", "font_id", key, "font_ref", fontRef, "error", err)
			// Skip this font if extraction fails
			continue
		}
		insertData(&ParsedFont{
			FontID: key,
			Data:   fontStreamBytes,
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
		intMediaBox := make([]int, 0, len(mediaBoxArray))
		for _, v := range mediaBoxArray {
			intV, ok := v.(int)
			if !ok {
				return nil, fmt.Errorf("MediaBox element is not int: %T", v)
			}
			intMediaBox = append(intMediaBox, intV)
		}
		return intMediaBox, nil
	}
	parentRef, found := findTargetRef(page, "Parent")
	if !found {
		return nil, errors.New("MediaBox and Parent not found in page object")
	}
	parent, err := p.ParseObject(parentRef)
	if err != nil {
		return nil, fmt.Errorf("failed to parse parent object for MediaBox: %w", err)
	}
	return p.GetMediaBox(parent)
}

func generateSequence(start, end, base int64) ([]int64, error) {
	if start <= 0 || end <= 0 || base <= 0 {
		return nil, fmt.Errorf("page numbers must be positive, got start=%d, end=%d, base=%d", start, end, base)
	}
	if start > end {
		return nil, fmt.Errorf("start page %d must be less than or equal to end page %d", start, end)
	}
	if base < start || base > end {
		return nil, fmt.Errorf("base page %d must be within the range [start %d, end %d]", base, start, end)
	}
	size := end - start + 1
	slice := make([]int64, size)
	for i := int64(0); i < size; i++ {
		slice[i] = start + i
	}
	abs := func(x int64) int64 {
		if x < 0 { return -x }
		return x
	}
	sort.Slice(slice, func(i, j int) bool {
		diffI := abs(slice[i] - base)
		diffJ := abs(slice[j] - base)
		if diffI != diffJ { return diffI < diffJ }
		return slice[i] < slice[j] // Preserve original order for same distance
	})
	return slice, nil
}

func normalizePageNum(start, end, base, pageLen int64) (int64, int64, int64) {
	if pageLen < 1 { pageLen = 1 }
	if start < 1 { start = 1 }
	if start > pageLen { start = pageLen }
	if base < 1 { base = 1 } // Base also needs to be at least 1
	if base < start { base = start }
	if base > pageLen { base = pageLen }
	if end < 1 { end = 1} // End also needs to be at least 1
	if end < base { end = base }
	if end > pageLen { end = pageLen }
	return start, end, base
}

func (p *PDFParser) GetCatalog() (*Catalog, error) {
	root, err := p.ParseObject(p.root)
	if err != nil {
		return nil, fmt.Errorf("failed to parse root object for catalog: %w", err)
	}
	pagesRef, found := findTargetRef(root, "Pages")
	if !found {
		return nil, errors.New("/Pages reference not found in root object")
	}
	return &Catalog{pagesRef}, nil
}

func (p *PDFParser) loadPageObject(catalogRef Catalog) error {
	pages, err := p.ParseObject(catalogRef.PagesRef)
	if err != nil {
		return fmt.Errorf("failed to parse Pages object from catalog ref %v: %w", catalogRef.PagesRef, err)
	}
	kids, found := findTargetRefs(pages, "Kids")
	if !found {
		// Check if this /Pages object is a single /Page object (Type /Page)
		objType, typeFound := findTarget(pages, "Type")
		if typeFound && objType == "Page" {
			return p.loadPerPageObject(catalogRef.PagesRef) // Process this node itself as a page
		}
		return errors.New("/Kids not found in /Pages object and it's not a /Page object itself")
	}
	for _, kid := range kids {
		err = p.loadPerPageObject(kid)
		if err != nil {
			return err // Error already has context from deeper calls
		}
	}
	return nil
}

func (p *PDFParser) loadPerPageObject(ptRef PDFRef) error {
	pt, err := p.ParseObject(ptRef)
	if err != nil {
		return fmt.Errorf("failed to parse page tree node object %v: %w", ptRef, err)
	}
	t, found := findTarget(pt, "Type")
	if !found {
		return fmt.Errorf("/Type not found in page tree node object %v", ptRef)
	}
	switch t {
	case "Pages": // Another page tree node
		kids, found := findTargetRefs(pt, "Kids")
		if !found {
			return fmt.Errorf("/Kids not found in /Pages node %v", ptRef)
		}
		for _, kid := range kids {
			err = p.loadPerPageObject(kid)
			if err != nil { return err }
		}
	case "Page":
		contentsRef, _ := findTargetRef(pt, "Contents") // Contents can be optional or an array
		resourcesRef, _ := findTargetRef(pt, "Resources") // Resources can be optional

		intMediaBox, err := p.GetMediaBox(pt)
		if err != nil {
			return fmt.Errorf("failed to get MediaBox for page %v: %w", ptRef, err)
		}
		if len(intMediaBox) < 4 {
			return fmt.Errorf("MediaBox for page %v is malformed (less than 4 elements): %v", ptRef, intMediaBox)
		}
		pageWidth := intMediaBox[2] - intMediaBox[0]
		pageHeight := intMediaBox[3] - intMediaBox[1]
		p.pageQueue = append(p.pageQueue, Page{contentsRef, resourcesRef, float64(pageWidth), float64(pageHeight)})
	default:
		return fmt.Errorf("unexpected type '%s' for page tree node %v", t, ptRef)
	}
	return nil
}

func (p *PDFParser) Close() error {
	if p.file != nil {
		return p.file.Close()
	}
	return nil
}

func (p *PDFParser) ExtractPage(pageNum int) (*Page, error) {
	if pageNum <= 0 || pageNum > len(p.pageQueue) {
		return nil, fmt.Errorf("page number %d out of range (1 to %d)", pageNum, len(p.pageQueue))
	}
	page := p.pageQueue[pageNum-1]
	return &page, nil
}

func (p *PDFParser) ExtractPageContents(contentsRef PDFRef, pageHeight float64) ([]TextCommand, []ImageCommand, []PathCommand, error) {
	if contentsRef == 0 { // No contents for this page
		return nil, nil, nil, nil
	}
	contents, err := p.ParseObject(contentsRef)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to parse page contents object %v: %w", contentsRef, err)
	}
	filter, _ := findTarget(contents, "Filter") // Filter might not be present

	contentsStreamBytes, err := p.ExtractStreamByRef(contentsRef)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to extract stream from page contents %v: %w", contentsRef, err)
	}

	if s, ok := filter.(string); ok && s == "FlateDecode" {
		decompressedBytes, err := deCompressStream(contentsStreamBytes)
		if err != nil {
			p.logger.Warn("Failed to decompress page content stream", "error", err, "ref", contentsRef)
			return nil, nil, nil, fmt.Errorf("failed to decompress page content stream %v: %w", contentsRef, err)
		}
		contentsStreamBytes = decompressedBytes
	}
	fontMap := make(map[string]map[byte]string)
	for _, font := range p.fonts {
		fontMap[font.FontID] = font.fontMap
	}
	to := NewTokenObject(string(contentsStreamBytes), fontMap)
	tc, ic, pc := to.ExtractCommands(pageHeight)
	return tc, ic, pc, nil
}

func (p *PDFParser) ExtractFont(resourceRef PDFRef) error {
	if resourceRef == 0 { return nil } // No resources, no fonts
	resources, err := p.ParseObject(resourceRef)
	if err != nil {
		return fmt.Errorf("failed to parse font resources object %v: %w", resourceRef, err)
	}
	fontsObj, found := findTarget(resources, "Font")
	if !found { return nil } // No /Font dictionary in resources

	fontsMap, ok := fontsObj.(map[string]PDFObject)
	if !ok { return errors.New("/Font in resources is not a dictionary") }

	for key, value := range fontsMap {
		fontRefStr, ok := value.(string)
		if !ok {
			p.logger.Warn("Font reference is not a string in /Font dictionary", "key", key, "type", fmt.Sprintf("%T",value))
			continue // Skip this font
		}
		fontRef, ok := parseRef(fontRefStr)
		if !ok {
			p.logger.Warn("Invalid font reference string", "key", key, "ref_string", fontRefStr)
			continue // Skip
		}
		font, err := p.ParseObject(fontRef)
		if err != nil {
			p.logger.Warn("Failed to parse font object", "key", key, "font_ref", fontRef, "error", err)
			continue // Skip
		}
		subType, _ := findTarget(font, "Subtype")
		if subType != "TrueType" && subType != "Type0" { // Assuming we only handle TrueType and Type0 for now
			p.logger.Debug("Skipping font of unsupported subtype", "key", key, "subtype", subType)
			continue
		}

		// Simplified: just get FontFile2 if available for TrueType
		// Full CMap and ToUnicode handling is complex
		if subType == "TrueType" {
			toUnicodeRef, foundToUnicode := findTargetRef(font, "ToUnicode")
			var cmaps map[byte]string
			if foundToUnicode {
				toUnicodeStreamBytes, errTUStream := p.ExtractStreamByRef(toUnicodeRef)
				if errTUStream != nil {
					p.logger.Warn("Failed to extract ToUnicode stream", "font_key", key, "ref", toUnicodeRef, "error", errTUStream)
				} else {
					toUnicodeObj, _ := p.ParseObject(toUnicodeRef) // already parsed for stream, but need for filter
					filterTU, _ := findTarget(toUnicodeObj, "Filter")
					if sTU, okTU := filterTU.(string); okTU && sTU == "FlateDecode" {
						decompTUBytes, errDCTU := deCompressStream(toUnicodeStreamBytes)
						if errDCTU != nil {
							p.logger.Warn("Failed to decompress ToUnicode stream", "font_key", key, "ref", toUnicodeRef, "error", errDCTU)
						} else {
							toUnicodeStreamBytes = decompTUBytes
						}
					}
					firstCharVal, _ := findTarget(font, "FirstChar")
					firstCharInt, _ := firstCharVal.(int) // Default to 0 if not found/not int
					cmaps, err = p.ExtractCMaps(string(toUnicodeStreamBytes), int8(firstCharInt))
					if err != nil {
						p.logger.Warn("Failed to extract CMaps", "font_key", key, "error", err)
					}
				}
			}


			fontFileRefVal := PDFRef(0)
			fontDescriptorRef, fdFound := findTargetRef(font, "FontDescriptor")
			if fdFound {
				fontDescriptor, errFD := p.ParseObject(fontDescriptorRef)
				if errFD != nil {
					p.logger.Warn("Failed to parse FontDescriptor", "font_key", key, "ref", fontDescriptorRef, "error", errFD)
				} else {
					ff2Ref, ff2Found := findTargetRef(fontDescriptor, "FontFile2")
					if ff2Found {
						fontFileRefVal = ff2Ref
					} else {
						p.logger.Debug("FontFile2 not found in FontDescriptor", "font_key", key)
					}
				}
			}
			p.fonts[key] = Font{FontID: key, FontDataRef: fontFileRefVal, fontMap: cmaps}
		}
	}
	return nil
}

func (p *PDFParser) ExtractImageRefs(resourceRef PDFRef) (map[string]PDFRef, error) {
	if resourceRef == 0 { return nil, nil }
	images := make(map[string]PDFRef)
	resources, err := p.ParseObject(resourceRef)
	if err != nil {
		return nil, fmt.Errorf("failed to parse image resources object %v: %w", resourceRef, err)
	}
	xObjects, found := findTarget(resources, "XObject")
	if !found { return images, nil } // No XObject dictionary

	imagesMap, ok := xObjects.(map[string]PDFObject)
	if !ok { return nil, errors.New("/XObject in resources is not a dictionary") }

	for key, value := range imagesMap {
		imageRefStr, okS := value.(string)
		if !okS {
			p.logger.Warn("XObject reference is not a string", "key", key, "type", fmt.Sprintf("%T", value))
			continue
		}
		imageRef, okP := parseRef(imageRefStr)
		if !okP {
			p.logger.Warn("Invalid XObject reference string", "key", key, "ref_string", imageRefStr)
			continue
		}
		// Further check if it's an image subtype, simplified here
		images[key] = imageRef
	}
	return images, nil
}

func (p *PDFParser) ExtractImageStream(imageRef PDFRef) (*ExtractedImage, error) {
	imageObj, err := p.ParseObject(imageRef)
	if err != nil {
		return nil, fmt.Errorf("failed to parse image object %v: %w", imageRef, err)
	}
	imageStreamBytes, err := p.ExtractStreamByRef(imageRef)
	if err != nil {
		return nil, fmt.Errorf("failed to extract stream for image %v: %w", imageRef, err)
	}

	imageFilterVal, _ := findTarget(imageObj, "Filter") // Filter might be an array or single name
	// Basic handling for single filter name, array needs more complex logic
	imageFilterStr, _ := imageFilterVal.(string)


	smaskStreamBytes := make([]byte, 0)
	smask, foundSMask := findTarget(imageObj, "SMask")
	if foundSMask {
		smaskRefStr, okS := smask.(string)
		if !okS { /* handle error or log */ } else {
			smaskRef, okP := parseRef(smaskRefStr)
			if !okP { /* handle error or log */ } else {
				smaskStreamBytes, err = p.ExtractStreamByRef(smaskRef)
				if err != nil {
					p.logger.Warn("Failed to extract SMask stream", "image_ref", imageRef, "smask_ref", smaskRef, "error", err)
					// Non-fatal for SMask?
				}
			}
		}
	}

	var ext string
	if imageFilterStr == "DCTDecode" { ext = "jpg"
	} else if imageFilterStr == "JPXDecode" { ext = "jp2" // JPEG2000
	} else { ext = "png" } // Default or for FlateDecode, LZWDecode etc.

	widthVal, wFound := findTarget(imageObj, "Width")
	heightVal, hFound := findTarget(imageObj, "Height")
	if !wFound || !hFound { return nil, fmt.Errorf("Width or Height not found for image %v", imageRef) }

	widthInt, okW := widthVal.(int)
	heightInt, okH := heightVal.(int)
	if !okW || !okH { return nil, fmt.Errorf("Width or Height not int for image %v (W: %T, H: %T)", imageRef, widthVal, heightVal) }

	return &ExtractedImage{
		Data: imageStreamBytes, MaskData: smaskStreamBytes, Width: float64(widthInt), Height: float64(heightInt), Ext: ext,
	}, nil
}

func (p *PDFParser) ExtractCMaps(cmapsString string, firstCharNumber int8) (map[byte]string, error) {
	re := regexp.MustCompile(`(?s)\d+\s+beginbfrange\s+(.*?)\s+endbfrange`)
	matches := re.FindAllStringSubmatch(cmapsString, -1)
	var substrings string
	for _, match := range matches {
		if len(match) > 1 { substrings = substrings + "\n" + match[1] }
	}
	values := make(map[byte]string)
	cmapsLines := strings.Split(substrings, "\n")
	cnt := int16(0) // Use int16 to avoid overflow with firstCharNumber + cnt
	for _, cmapLine := range cmapsLines {
		trimmedLine := strings.TrimSpace(cmapLine)
		if trimmedLine == "" { continue }
		split := strings.Split(strings.Trim(strings.Trim(trimmedLine, "<"), ">"), "><")
		if len(split) != 3 { continue } // Malformed line

		startIndex, errS := strconv.ParseInt(split[0], 16, 64)
		endIndex, errE := strconv.ParseInt(split[1], 16, 64)
		valueHex, errV := strconv.ParseInt(split[2], 16, 64)
		if errS != nil || errE != nil || errV != nil {
			p.logger.Warn("Error parsing cmap bfrange line", "line", cmapLine, "start_err", errS, "end_err", errE, "val_err", errV)
			continue
		}
		for i := int64(0); i <= endIndex-startIndex; i++ {
			mapIndex := byte(int16(firstCharNumber) + cnt)
			values[mapIndex] = string(rune(int(valueHex) + int(i)))
			cnt++
		}
	}
	return values, nil
}

func (p *PDFParser) ExtractFontStream(fontRef PDFRef) ([]byte, error) {
	fontObject, err := p.ParseObject(fontRef)
	if err != nil {
		return nil, fmt.Errorf("failed to parse font object %v: %w", fontRef, err)
	}
	fontStreamBytes, err := p.ExtractStreamByRef(fontRef)
	if err != nil {
		return nil, fmt.Errorf("failed to extract stream for font %v: %w", fontRef, err)
	}

	fontFilterVal, _ := findTarget(fontObject, "Filter")
	if s, ok := fontFilterVal.(string); ok && s == "FlateDecode" {
		decompressedBytes, errDC := deCompressStream(fontStreamBytes)
		if errDC != nil {
			p.logger.Warn("Failed to decompress font stream", "error", errDC, "fontRef", fontRef)
			return nil, fmt.Errorf("failed to decompress font stream %v: %w", fontRef, errDC)
		}
		fontStreamBytes = decompressedBytes
	}

	fontLength1Val, foundL1 := findTarget(fontObject, "Length1")
	if foundL1 {
		fontLength1Int, okL1 := fontLength1Val.(int)
		if !okL1 {
			return nil, fmt.Errorf("font Length1 is not int for %v (got %T)", fontRef, fontLength1Val)
		}
		if fontLength1Int < 0 || fontLength1Int > len(fontStreamBytes) {
			return nil, fmt.Errorf("invalid font Length1 %d for stream length %d, ref %v", fontLength1Int, len(fontStreamBytes), fontRef)
		}
		fontStreamBytes = fontStreamBytes[:fontLength1Int]
	}
	return fontStreamBytes, nil
}

func (p *PDFParser) ExtractStreamByRef(ref PDFRef) ([]byte, error) {
	objectInfo, ok := p.xrefTable[ref]
	if !ok {
		return nil, fmt.Errorf("object ref %d not found in xref table for stream extraction", ref)
	}
	objectString, err := loadObject(p.file, objectInfo.offsetByte)
	if err != nil {
		return nil, fmt.Errorf("failed to load object dictionary for stream %v: %w", ref, err)
	}
	dictObject, err := parseMetadata(objectString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse metadata for stream object %v dictionary: %w", ref, err)
	}

	lengthVal, found := findTarget(dictObject, "Length")
	if !found { return nil, fmt.Errorf("stream /Length not found for object %v", ref) }
	lengthInt, ok := lengthVal.(int)
	if !ok { return nil, fmt.Errorf("stream /Length is not int for object %v (got %T)", ref, lengthVal) }
	if lengthInt < 0 { return nil, fmt.Errorf("invalid stream length %d for object %v", lengthInt, ref) }
	if lengthInt == 0 { return []byte{}, nil }

	// Seek to the beginning of the object to find the "stream" keyword reliably.
	if _, errSeek := p.file.Seek(objectInfo.offsetByte, io.SeekStart); errSeek != nil {
		return nil, fmt.Errorf("failed to re-seek to object %v for stream reading: %w", ref, errSeek)
	}

	scanner := bufio.NewScanner(p.file)
	var streamDataStartOffset int64 = -1
	var bytesScannedThisObject int64 = 0

	for scanner.Scan() {
		line := scanner.Text()
		currentLineLength := int64(len(line) + 1) // +1 for \n

		if strings.TrimSpace(line) == "stream" {
			streamDataStartOffset = objectInfo.offsetByte + bytesScannedThisObject + currentLineLength
			break
		}
		bytesScannedThisObject += currentLineLength
		// Heuristic: objectString is dict part. stream keyword should be shortly after.
		if bytesScannedThisObject > int64(len(objectString)) + 200 { // 200 as margin
			return nil, fmt.Errorf("could not find 'stream' keyword for object %v within reasonable bounds", ref)
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		return nil, fmt.Errorf("error scanning for 'stream' keyword for object %v: %w", ref, errScan)
	}
	if streamDataStartOffset == -1 {
		return nil, fmt.Errorf("'stream' keyword not found for object %v", ref)
	}

	if _, errSeek := p.file.Seek(streamDataStartOffset, io.SeekStart); errSeek != nil {
		return nil, fmt.Errorf("failed to seek to stream data start for %v (offset %d): %w", ref, streamDataStartOffset, errSeek)
	}

	buffer := make([]byte, lengthInt)
	bytesRead, errRead := io.ReadFull(p.file, buffer)
	if errRead != nil {
		if errRead == io.ErrUnexpectedEOF { // Read fewer bytes than specified Length
			p.logger.Info("Stream data shorter than specified Length", "ref", ref, "expected", lengthInt, "actual", bytesRead)
			return buffer[:bytesRead], nil // Return what was read
		}
		return nil, fmt.Errorf("failed to read stream content for %v (requested %d): %w", ref, lengthInt, errRead)
	}
	return buffer, nil
}

func deCompressStream(buffer []byte) ([]byte, error) {
	if len(buffer) == 0 { return []byte{}, nil }
	fr, err := zlib.NewReader(bytes.NewReader(buffer))
	if err != nil {
		return nil, fmt.Errorf("%w when creating zlib reader: %v", ErrParserDeCompressionError, err)
	}
	defer fr.Close()
	var decompressedData bytes.Buffer
	if _, err = io.Copy(&decompressedData, fr); err != nil {
		return nil, fmt.Errorf("failed to decompress data using zlib: %w", err)
	}
	return decompressedData.Bytes(), nil
}

func parseXrefTable(file IPDFFile, logger *slog.Logger) (map[PDFRef]XRefTableElement, *string, error) {
	xrefTableOffsetByte, err := getXrefTableOffsetByte(file, logger)
	if err != nil {
		return nil, nil, fmt.Errorf("could not get xref table offset: %w", err)
	}
	if _, err = file.Seek(int64(xrefTableOffsetByte), io.SeekStart); err != nil {
		return nil, nil, fmt.Errorf("failed to seek to xref table offset %d: %w", xrefTableOffsetByte, err)
	}
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() || scanner.Text() != "xref" { // Read "xref"
		return nil, nil, fmt.Errorf("xref keyword not found at offset %d (read: '%s')", xrefTableOffsetByte, scanner.Text())
	}
	if !scanner.Scan() { // Read "startObj numEntries"
		return nil, nil, errors.New("failed to read xref section header line")
	}
	line := scanner.Text()
	parts := strings.Fields(line)
	if len(parts) != 2 { return nil, nil, fmt.Errorf("xref section header format error: '%s'", line) }
	startObjNum, errS := strconv.Atoi(parts[0])
	numEntries, errN := strconv.Atoi(parts[1])
	if errS != nil || errN != nil {
		return nil, nil, fmt.Errorf("error parsing xref section header '%s': start_err=%v, num_err=%v", line, errS, errN)
	}

	xrefTable := make(map[PDFRef]XRefTableElement, numEntries)
	for i := 0; i < numEntries; i++ {
		objNum := PDFRef(startObjNum + i)
		if !scanner.Scan() {
			return nil, nil, fmt.Errorf("xref table ended prematurely; expected entry for object %d", objNum)
		}
		entryLine := scanner.Text()
		if strings.TrimSpace(entryLine) == "trailer" { // End of this xref subsection
			numEntries = i // Update numEntries to actual count
			break
		}
		entryParts := strings.Fields(entryLine)
		if len(entryParts) != 3 { return nil, nil, fmt.Errorf("xref entry for obj %d format error: '%s'", objNum, entryLine) }
		offset, errOff := strconv.ParseInt(entryParts[0], 10, 64)
		gen, errGen := strconv.Atoi(entryParts[1])
		state := entryParts[2]
		if errOff != nil || errGen != nil {
			return nil, nil, fmt.Errorf("error parsing xref entry for obj %d ('%s'): offset_err=%v, gen_err=%v", objNum, entryLine, errOff, errGen)
		}
		if state == "n" { // In-use entry
			xrefTable[objNum] = XRefTableElement{ObjNum: objNum, GenNum: PDFRef(gen), offsetByte: offset}
		}
	}

	var trailerDictBuf strings.Builder
	inTrailerDict := false
	for scanner.Scan() {
		line = scanner.Text()
		if strings.TrimSpace(line) == "trailer" {
			inTrailerDict = true
			continue
		}
		if inTrailerDict {
			trailerDictBuf.WriteString(line + "\n")
			if strings.Contains(line, ">>") { break } // End of trailer dict
		}
	}
	if err = scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("error scanning for trailer dictionary: %w", err)
	}
	trailerStr := strings.TrimSpace(trailerDictBuf.String())
	if trailerStr == "" { return nil, nil, errors.New("trailer dictionary not found or empty") }
	return xrefTable, &trailerStr, nil
}

func getXrefTableOffsetByte(file IPDFFile, logger *slog.Logger) (int, error) {
	const seekBufSize = 256
	fileSize, err := file.Seek(0, io.SeekEnd)
	if err != nil { return 0, fmt.Errorf("failed to seek to end of file: %w", err) }
	if fileSize == 0 { return 0, errors.New("file is empty") }

	readOffset := fileSize - int64(seekBufSize)
	if readOffset < 0 { readOffset = 0 }

	bufLen := int(fileSize - readOffset)
	buffer := make([]byte, bufLen)

	if _, err = file.Seek(readOffset, io.SeekStart); err != nil {
		return 0, fmt.Errorf("failed to seek for startxref search (offset %d): %w", readOffset, err)
	}
	bytesRead, err := io.ReadFull(file, buffer)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF { // EOFs are okay if some bytes were read
		logger.Warn("Error reading end of file for startxref search", "error", err, "bytes_read", bytesRead)
		if bytesRead == 0 { return 0, fmt.Errorf("failed to read end of file for startxref (read 0 bytes): %w", err) }
	}
	content := string(buffer[:bytesRead])

	startxrefIdx := strings.LastIndex(content, "startxref")
	if startxrefIdx == -1 {
		logger.Debug("startxref keyword not found", "filesize", fileSize, "searched_content_snippet", firstN(content, 50))
		return 0, errors.New("startxref keyword not found")
	}

	offsetStrStart := startxrefIdx + len("startxref")
	if offsetStrStart >= len(content) { return 0, errors.New("no content after 'startxref' keyword") }

	searchStr := content[offsetStrStart:]
	scanner := bufio.NewScanner(strings.NewReader(searchStr))
	for scanner.Scan() {
		line = strings.TrimSpace(scanner.Text())
		if line == "" { continue }
		offset, errAtoi := strconv.Atoi(line)
		if errAtoi == nil {
			if int64(offset) >= fileSize || offset < 0 {
				logger.Error("Parsed startxref offset out of file bounds", "offset", offset, "file_size", fileSize)
				return 0, fmt.Errorf("startxref offset %d out of file bounds (size %d)", offset, fileSize)
			}
			return offset, nil
		}
		logger.Debug("Failed to parse int from line after 'startxref'", "line", line, "error", errAtoi)
	}
	if errScan := scanner.Err(); errScan != nil {
		return 0, fmt.Errorf("error scanning for xref offset value: %w", errScan)
	}
	return 0, errors.New("could not parse xref offset value after 'startxref'")
}

//nolint:all
func parseMetadata(objectString string) (PDFObject, error) {
	objectString = strings.TrimSpace(objectString)
	if objectString == "" {
		return nil, fmt.Errorf("cannot parse empty object string")
	}
	if strings.HasPrefix(objectString, "<<") && strings.HasSuffix(objectString, ">>") {
		return parseDict(objectString)
	} else if strings.HasPrefix(objectString, "[") && strings.HasSuffix(objectString, "]") {
		return parseArray(objectString)
	} else if i, err := strconv.Atoi(objectString); err == nil {
		return i, nil
	} else if f, err := strconv.ParseFloat(objectString, 64); err == nil {
		return f, nil
	} else if objectString == "true" {
		return true, nil
	} else if objectString == "false" {
		return false, nil
	} else if objectString == "null" {
		return nil, nil
	} else if strings.HasPrefix(objectString, "/") {
		return objectString, nil
	} else if সম্ভবনাIsRef(objectString) {
		return objectString, nil
	} else {
		return nil, fmt.Errorf("parse error: Unknown type for string '%s'", objectString)
	}
}

func সম্ভবনাIsRef(s string) bool {
	parts := strings.Fields(s)
	if len(parts) == 3 {
		if _, err1 := strconv.Atoi(parts[0]); err1 == nil {
			if _, err2 := strconv.Atoi(parts[1]); err2 == nil {
				if parts[2] == "R" {
					return true
				}
			}
		}
	}
	return false
}

func parseDict(dictString string) (map[string]PDFObject, error) {
	dict := make(map[string]PDFObject)
	trimmedContent := strings.TrimSpace(dictString)
	if !strings.HasPrefix(trimmedContent, "<<") || !strings.HasSuffix(trimmedContent, ">>") {
		return nil, fmt.Errorf("invalid dictionary format: missing '<<' or '>>': %s", dictString)
	}
	content := strings.TrimSpace(trimmedContent[2 : len(trimmedContent)-2])
	if content == "" { return dict, nil }

	reader := bufio.NewReader(strings.NewReader(content))
	var key string
	for {
		token, err := readNextToken(reader)
		if err == io.EOF {
			if key != "" { return nil, fmt.Errorf("dictionary ended with unfulfilled key '%s'", key) }
			break
		}
		if err != nil { return nil, fmt.Errorf("failed to read token in dictionary ('%s'): %w", content, err) }

		processedToken := strings.TrimSpace(token)
		if processedToken == "" { continue }

		if key == "" {
			if !strings.HasPrefix(processedToken, "/") {
				return nil, fmt.Errorf("invalid dictionary key '%s', must start with '/'", processedToken)
			}
			key = processedToken
		} else {
			value, errVal := parseMetadata(processedToken)
			if errVal != nil {
				return nil, fmt.Errorf("failed to parse value for dict key '%s' (token '%s'): %w", key, processedToken, errVal)
			}
			dict[key] = value
			key = ""
		}
	}
	return dict, nil
}

func parseArray(arrayString string) ([]PDFObject, error) {
	var array []PDFObject
	trimmedContent := strings.TrimSpace(arrayString)
	if !strings.HasPrefix(trimmedContent, "[") || !strings.HasSuffix(trimmedContent, "]") {
		return nil, fmt.Errorf("invalid array format: missing '[' or ']': %s", arrayString)
	}
	content := strings.TrimSpace(trimmedContent[1 : len(trimmedContent)-1])
	if content == "" { return array, nil }

	reader := bufio.NewReader(strings.NewReader(content))
	for {
		token, err := readNextToken(reader)
		if err == io.EOF { break }
		if err != nil { return nil, fmt.Errorf("failed to read token in array ('%s'): %w", content, err) }

		processedToken := strings.TrimSpace(token)
		if processedToken == "" { continue }
		value, errVal := parseMetadata(processedToken)
		if errVal != nil {
			return nil, fmt.Errorf("failed to parse array element (token '%s'): %w", processedToken, errVal)
		}
		array = append(array, value)
	}
	return array, nil
}

func readNextToken(reader *bufio.Reader) (string, error) {
    var token strings.Builder
    inLiteralString := false
    nestingDict := 0
    nestingArray := 0

    for {
        r, _, err := reader.ReadRune()
        if err != nil {
            if err == io.EOF {
                if token.Len() > 0 { return token.String(), nil }
                return "", io.EOF
            }
            return "", err
        }

        if r == '(' && nestingDict == 0 && nestingArray == 0 {
            inLiteralString = true
        } else if r == ')' && inLiteralString {
            inLiteralString = false
            token.WriteRune(r)
            return token.String(), nil
        }

        if inLiteralString {
            token.WriteRune(r)
            continue
        }

        if r == '<' {
            nextRune, _, _ := reader.ReadRune()
            if nextRune == '<' {
                if token.Len() > 0 && nestingDict == 0 && nestingArray == 0 {
                    reader.UnreadRune(); reader.UnreadRune()
                    return token.String(), nil
                }
                token.WriteRune(r); token.WriteRune(nextRune)
                nestingDict++
                continue
            }
            reader.UnreadRune()
        } else if r == '>' {
            nextRune, _, _ := reader.ReadRune()
            if nextRune == '>' {
                token.WriteRune(r); token.WriteRune(nextRune)
                nestingDict--
                if nestingDict == 0 && nestingArray == 0 { return token.String(), nil }
                continue
            }
            reader.UnreadRune()
        }

        if r == '[' {
            if token.Len() > 0 && nestingDict == 0 && nestingArray == 0 {
                reader.UnreadRune()
                return token.String(), nil
            }
            token.WriteRune(r)
            nestingArray++
            continue
        } else if r == ']' {
            token.WriteRune(r)
            nestingArray--
            if nestingArray == 0 && nestingDict == 0 { return token.String(), nil }
            continue
        }

        if (r == ' ' || r == '\n' || r == '\r' || r == '\t') && nestingDict == 0 && nestingArray == 0 {
            if token.Len() > 0 { return token.String(), nil }
            continue
        }

        if r == '/' && nestingDict == 0 && nestingArray == 0 {
             if token.Len() > 0 {
                reader.UnreadRune()
                return token.String(), nil
            }
        }
        token.WriteRune(r)
    }
}

func findTarget(obj PDFObject, target string) (PDFObject, bool) {
	dict, ok := obj.(map[string]PDFObject)
	if !ok { return nil, false }
	val, found := dict[target]
	return val, found
}

func findTargetRef(obj PDFObject, target string) (PDFRef, bool) {
	val, found := findTarget(obj, target)
	if !found { return 0, false }
	refStr, ok := val.(string)
	if !ok { return 0, false }
	ref, okP := parseRef(refStr)
	if !okP { return 0, false }
	return ref, true
}

func findTargetRefs(obj PDFObject, target string) ([]PDFRef, bool) {
	val, found := findTarget(obj, target)
	if !found { return nil, false }
	arr, ok := val.([]PDFObject)
	if !ok { return nil, false }
	var refs []PDFRef
	for _, item := range arr {
		refStr, okS := item.(string)
		if !okS { return nil, false }
		ref, okP := parseRef(refStr)
		if !okP { return nil, false }
		refs = append(refs, ref)
	}
	return refs, true
}

func parseRef(refString string) (PDFRef, bool) {
	parts := strings.Fields(refString)
	if len(parts) != 3 || parts[2] != "R" { return 0, false }
	objNum, err := strconv.Atoi(parts[0])
	if err != nil { return 0, false }
	return PDFRef(objNum), true
}

// Helper to get first N runes of a string, for logging snippets.
func firstN(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

var ErrParserDeCompressionError = errors.New("parser: decompression error")
// var ErrParserParseObjectError = errors.New("parser: parse object error") // No longer used directly
// var ErrParserReadStreamError = errors.New("parser: read stream error") // No longer used directly
