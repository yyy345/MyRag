package ports

import (
	"context"
	"errors"
)

const MaxImageBytes = 5 * 1024 * 1024

var ErrInvalidImage = errors.New("图片格式无效或超过大小限制")
var ErrStorageUnavailable = errors.New("图片存储暂不可用")

// Keys are derived exclusively from authenticated user and server-generated IDs.
type ImageStorage interface {
	PutImage(context.Context, string, string, []byte, string) (string, error)
	DeleteImage(context.Context, string, string) error
}

type ImageDescriber interface {
	DescribeImage(context.Context, string, []byte, string) (string, error)
}
