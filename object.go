package pdtp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"runtime/debug"
	"strconv"
	"strings"
	"unicode"
)

type PDFObject interface{}

func findTarget(obj PDFObject, target string) (PDFObject, bool) {
	switch expression := obj.(type) {
	case map[string]PDFObject:
		if pagesValue, exists := expression[target]; exists {
			return pagesValue, true
		}
		// 辞書内の値を再帰的に探索
		for _, value := range expression {
			if result, found := findTarget(value, target); found {
				return result, true
			}
		}
	}
	return nil, false
}

func findTargetRef(obj PDFObject, target string) (PDFRef, bool) {
	if targetObj, found := findTarget(obj, target); found {
		if ref, ok := targetObj.(string); ok {
			return parseRef(ref)
		}
	}
	return 0, false
}

func findTargetRefs(obj PDFObject, target string) ([]PDFRef, bool) {
	if targetObj, found := findTarget(obj, target); found {
		if arr, ok := targetObj.([]PDFObject); ok {
			var refs []PDFRef
			for _, obj := range arr {
				if ref, ok := obj.(string); ok {
					if r, ok := parseRef(ref); ok {
						refs = append(refs, r)
					}
				}
			}
			return refs, true
		}
	}
	return nil, false
}

func parseRef(refString string) (PDFRef, bool) {
	refParts := strings.Split(refString, " ")
	if len(refParts) != 3 {
		return 0, false
	}
	num, err := strconv.Atoi(refParts[0])
	if err != nil {
		return 0, false
	}

	return PDFRef(num), true
}

func parseMetadata(objectString string) (PDFObject, error) {
	m := strings.TrimSpace(objectString)
	if !strings.HasPrefix(m, "<<") || !strings.HasSuffix(m, ">>") {
		log.Println(string(debug.Stack()))
		return nil, errors.New("object format is not correct")
	}
	reader := strings.NewReader(m)
	obj, err := parseObject(reader)
	if err != nil {
		return nil, fmt.Errorf("メタデータの解析に失敗しました: %w", err)
	}
	return obj, nil
}

func parseObject(r io.RuneScanner) (PDFObject, error) {
	skipSpaces(r)
	ch, _, err := r.ReadRune()
	if err != nil {
		return nil, err
	}

	switch ch {
	case '<':
		nextCh, _, err := r.ReadRune()
		if err != nil {
			return nil, err
		}
		if nextCh == '<' {
			return parseDict(r)
		} else {
			r.UnreadRune()
			return parseHexString(r)
		}
	case '(':
		return parseLiteralString(r)
	case '/':
		return parseName(r)
	case '[':
		return parseArray(r)
	default:
		if unicode.IsDigit(ch) || ch == '-' || ch == '+' || ch == '.' {
			r.UnreadRune()
			return parseNumberOrRef(r)
		} else {
			r.UnreadRune()
			return parseKeyword(r)
		}
	}
}

func parseDict(r io.RuneScanner) (map[string]PDFObject, error) {
	dict := make(map[string]PDFObject)

	for {
		skipSpaces(r)
		ch, _, err := r.ReadRune()
		if err != nil {
			return nil, err
		}
		if ch == '>' {
			nextCh, _, err := r.ReadRune()
			if err != nil {
				return nil, err
			}
			if nextCh == '>' {
				break
			} else {
				return nil, errors.New(fmt.Sprintf("辞書の終了 '>>' が期待されましたが、'%c' が見つかりました", nextCh))
			}
		} else if ch == '/' {
			key, err := parseName(r)
			if err != nil {
				return nil, err
			}
			val, err := parseObject(r)
			if err != nil {
				return nil, err
			}
			dict[key.(string)] = val
		} else {
			return nil, errors.New(fmt.Sprintf("無効な辞書キーの開始文字: '%c'", ch))
		}
	}
	return dict, nil
}

func parseName(r io.RuneScanner) (PDFObject, error) {
	var buf bytes.Buffer
	for {
		ch, _, err := r.ReadRune()
		if err != nil {
			break
		}
		if isDelimiter(ch) || isWhiteSpace(ch) {
			r.UnreadRune()
			break
		}
		buf.WriteRune(ch)
	}
	return buf.String(), nil
}

func parseLiteralString(r io.RuneScanner) (string, error) {
	var buf bytes.Buffer
	depth := 1
	for {
		ch, _, err := r.ReadRune()
		if err != nil {
			return "", err
		}
		if ch == '(' {
			depth++
		} else if ch == ')' {
			depth--
			if depth == 0 {
				break
			}
		} else if ch == '\\' {
			nextCh, _, err := r.ReadRune()
			if err != nil {
				return "", err
			}
			buf.WriteRune(nextCh)
			continue
		}
		buf.WriteRune(ch)
	}
	return buf.String(), nil
}

func parseHexString(r io.RuneScanner) (string, error) {
	var buf bytes.Buffer
	for {
		ch, _, err := r.ReadRune()
		if err != nil {
			return "", err
		}
		if ch == '>' {
			break
		}
		buf.WriteRune(ch)
	}
	return buf.String(), nil
}

func parseArray(r io.RuneScanner) ([]PDFObject, error) {
	var arr []PDFObject
	for {
		skipSpaces(r)
		ch, _, err := r.ReadRune()
		if err != nil {
			return nil, err
		}
		if ch == ']' {
			break
		}
		r.UnreadRune()
		obj, err := parseObject(r)
		if err != nil {
			return nil, err
		}
		arr = append(arr, obj)
	}
	return arr, nil
}

func parseNumberOrRef(r io.RuneScanner) (PDFObject, error) {
	var buf bytes.Buffer
	for {
		ch, _, err := r.ReadRune()
		if err != nil {
			break
		}
		if isDelimiter(ch) || isWhiteSpace(ch) {
			r.UnreadRune()
			break
		}
		buf.WriteRune(ch)
	}
	token := buf.String()

	num1, err := parseNumber(token)
	if err != nil {
		return nil, err
	}

	pos, _ := r.(*strings.Reader).Seek(0, io.SeekCurrent)

	skipSpaces(r)
	ch, _, err := r.ReadRune()
	if err != nil {
		return num1, nil
	}

	if unicode.IsDigit(ch) {
		var buf2 bytes.Buffer
		buf2.WriteRune(ch)
		for {
			chNext, _, err := r.ReadRune()
			if err != nil {
				break
			}
			if isDelimiter(chNext) || isWhiteSpace(chNext) {
				r.UnreadRune()
				break
			}
			buf2.WriteRune(chNext)
		}
		token2 := buf2.String()

		skipSpaces(r)
		ch, _, err = r.ReadRune()
		if err != nil {
			r.(*strings.Reader).Seek(pos, io.SeekStart)
			return num1, nil
		}
		if ch == 'R' {
			num2, err := parseNumber(token2)
			if err != nil {
				return nil, err
			}
			return fmt.Sprintf("%v %v R", num1, num2), nil
		} else {
			r.(*strings.Reader).Seek(pos, io.SeekStart)
			return num1, nil
		}
	} else {
		r.(*strings.Reader).Seek(pos, io.SeekStart)
		return num1, nil
	}
}

func parseNumber(s string) (PDFObject, error) {
	if strings.Contains(s, ".") {
		return strconv.ParseFloat(s, 64)
	}
	return strconv.Atoi(s)
}

func parseKeyword(r io.RuneScanner) (PDFObject, error) {
	var buf bytes.Buffer
	for {
		ch, _, err := r.ReadRune()
		if err != nil {
			break
		}
		if isDelimiter(ch) || isWhiteSpace(ch) {
			r.UnreadRune()
			break
		}
		buf.WriteRune(ch)
	}
	token := buf.String()
	switch token {
	case "null":
		return nil, nil
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return token, nil
	}
}

func skipSpaces(r io.RuneScanner) {
	for {
		ch, _, err := r.ReadRune()
		if err != nil {
			break
		}
		if !isWhiteSpace(ch) {
			r.UnreadRune()
			break
		}
	}
}

func isWhiteSpace(ch rune) bool {
	return unicode.IsSpace(ch)
}

func isDelimiter(ch rune) bool {
	delimiters := "()<>[]{}/%"
	return strings.ContainsRune(delimiters, ch)
}
