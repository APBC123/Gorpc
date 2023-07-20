package codec

import (
	"io"
)

type Header struct {
	ServiceMethod string //Service.Method组合调用形式
	Seq           uint64 //请求的序列号，用于区分不同的请求
	Error         string
}

type Codec interface {
	io.Closer
	ReadHeader(*Header) error
	ReadBody(interface{}) error
	Write(*Header, interface{}) error
}

type NewCodecFunc func(conn io.ReadWriteCloser) Codec

type Type string

const (
	GobType  Type = "application/gob"
	JsonType Type = "application/json"
	XMLType  Type = "application/xml"
	//加入protobuf支持
)

var NewCodecFuncMap map[Type]NewCodecFunc

func init() {
	NewCodecFuncMap = make(map[Type]NewCodecFunc)
	NewCodecFuncMap[GobType] = NewGobCodec
	NewCodecFuncMap[JsonType] = NewJsonCodec
	NewCodecFuncMap[XMLType] = NewXMLCodec
}
