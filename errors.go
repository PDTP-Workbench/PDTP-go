package pdtp

import (
	"errors"
)

var (
	ErrParserDeCompressionError = errors.New("decompression error")
	ErrParserParseObjectError   = errors.New("parse object error")
	ErrParserReadStreamError    = errors.New("read stream error")
)
