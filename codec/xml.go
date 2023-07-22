package codec

import (
	"bufio"
	"encoding/xml"
	"io"
	"log"
)

type XMLCodec struct {
	conn io.ReadWriteCloser
	buf  *bufio.Writer
	enc  *xml.Encoder
	dec  *xml.Decoder
}

func NewXMLCodec(conn io.ReadWriteCloser) Codec {
	buf := bufio.NewWriter(conn)
	return &XMLCodec{
		conn: conn,
		buf:  buf,
		enc:  xml.NewEncoder(buf),
		dec:  xml.NewDecoder(conn),
	}
}

func (c *XMLCodec) ReadHeader(h *Header) error {
	return c.dec.Decode(h)
}

func (c *XMLCodec) ReadBody(body interface{}) error {
	return c.dec.Decode(body)
}

func (c *XMLCodec) Write(h *Header, body interface{}) (err error) {
	defer func() {
		_ = c.buf.Flush()
		if err != nil {
			_ = c.Close()
		}
	}()
	if err = c.enc.Encode(h); err != nil {
		log.Println("rpc codec: xml error encoding header:", err)
		return err
	}
	if err = c.enc.Encode(body); err != nil {
		log.Println("rpc codec: xml error encoding body:", err)
		return err
	}
	return nil
}

func (c *XMLCodec) Close() error {
	return c.conn.Close()
}
