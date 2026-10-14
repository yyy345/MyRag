package ports

import (
	"context"
	"errors"
	"io"
)

const MaxDocumentBytes = 20 * 1024 * 1024
const MaxParsedCharacters = 2_000_000

// The request ID is also the new index task's trace ID. Parsing never writes
// business records; the caller commits only a validated successful result.
// The reader contains the already size-checked, in-memory uploaded file.
type DocumentParser interface {
	Parse(context.Context, string, string, io.Reader) (string, error)
}

var (
	ErrDocumentTooLarge   = errors.New("文件过大，最大支持20MB")
	ErrParsedTextTooLarge = errors.New("文档解析后的内容过长，请拆分文件")
	ErrParseRejected      = errors.New("文档解析失败，请检查文件内容或格式")
	ErrParserUnavailable  = errors.New("文件处理服务暂不可用，请稍后重试")
	ErrParserTimeout      = errors.New("文件处理超时，请稍后重试")
)
