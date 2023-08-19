package gorpc

import (
	"Gorpc/codec"
	workpool "Gorpc/pool"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

type Call struct {
	Seq           uint64
	ServiceMethod string
	Args          interface{}
	Reply         interface{}
	Error         error
	Done          chan *Call
}

func (call *Call) done() {
	call.Done <- call
}

type clientResult struct {
	client *Client
	err    error
}

type newClientFunc func(conn net.Conn, opt *Option) (client *Client, err error)

type Client struct {
	cc       codec.Codec
	opt      *Option
	sending  sync.Mutex //用于保证请求有序发送
	header   codec.Header
	mu       sync.Mutex       //用于保证在Client对属性进行正常变更,如注册、移除服务等操作
	seq      uint64           //用于给call的seq赋值
	pending  map[uint64]*Call //暂存未处理完成的请求
	closing  bool
	shutdown bool
}

func (client *Client) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing {
		return errors.New("connection is shut down")
	}
	client.closing = true
	return client.cc.Close()
}

func (client *Client) IsAvailable() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return !(client.shutdown || client.closing)
}

func (client *Client) registerCall(call *Call) (uint64, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	//不应直接调用IsAvailable()函数，否则将发生死锁
	if client.closing || client.shutdown {
		return 0, errors.New("connection is shut down")
	}
	call.Seq = client.seq
	client.pending[call.Seq] = call
	client.seq++
	return call.Seq, nil
}

func (client *Client) removeCall(seq uint64) *Call {
	client.mu.Lock()
	defer client.mu.Unlock()
	call := client.pending[seq]
	delete(client.pending, seq)
	return call
}

func (client *Client) terminateCalls(err error) { //终止调用
	client.sending.Lock()
	defer client.sending.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	client.shutdown = true
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}

func (client *Client) receive() {
	var err error
	for err == nil {
		var h codec.Header
		if err = client.cc.ReadHeader(&h); err != nil {
			break
		}
		call := client.removeCall(h.Seq)
		switch {
		case call == nil:
			//一般为call被移除
			err = client.cc.ReadBody(nil)
		case h.Error != "":
			call.Error = fmt.Errorf(h.Error)
			err = client.cc.ReadBody(nil)
			call.done()
		default:
			err = client.cc.ReadBody(call.Reply)
			if err != nil {
				call.Error = errors.New("reading body " + err.Error())
			}
			call.done()
		}
	}
	client.terminateCalls(err) //存在err时终止call
}

func NewClient(conn net.Conn, opt *Option) (*Client, error) {
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		err := fmt.Errorf("invalid codec type %s", opt.CodecType)
		log.Println("rpc client: codec error:", err)
		return nil, err
	}
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Println("rpc client option error: ", err)
		_ = conn.Close()
		return nil, err
	}
	return newClientCodec(f(conn), opt), nil
}

// 创建客户端实例
func newClientCodec(cc codec.Codec, opt *Option) *Client {
	client := &Client{
		seq:     1,
		pending: make(map[uint64]*Call),
		opt:     opt,
		cc:      cc,
	}
	workpool.SubmitTask(func() {
		client.receive()
	})
	return client
}

// 主要是将option设为可选参数
func parseOptions(opts ...*Option) (*Option, error) {
	if len(opts) == 0 || opts[0] == nil {
		return DefaultOption, nil
	}
	if len(opts) != 1 {
		return nil, errors.New("number of options is more than 1")
	}
	opt := opts[0]
	opt.FlagNumber = DefaultOption.FlagNumber
	if opt.CodecType == "" {
		opt.CodecType = DefaultOption.CodecType
	}
	return opt, nil
}

func Dial(network, addr string, opts ...*Option) (client *Client, err error) {
	return dialTimeout(NewClient, network, addr, opts...)
}

func dialTimeout(f newClientFunc, network, addr string, opts ...*Option) (client *Client, err error) {
	opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout(network, addr, opt.ConnectionTimeout)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()
	ch := make(chan clientResult)
	workpool.SubmitTask(func() {
		client, err = f(conn, opt)
		ch <- clientResult{client: client, err: err}
	})
	if opt.ConnectionTimeout == 0 {
		result := <-ch
		return result.client, result.err
	}
	select {
	case <-time.After(opt.ConnectionTimeout):
		return nil, fmt.Errorf("rpc client: connect timeout")
	case result := <-ch:
		return result.client, result.err
	}
}
func (client *Client) send(call *Call) {
	client.sending.Lock()
	defer client.sending.Unlock()
	seq, err := client.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return
	}
	client.header.ServiceMethod = call.ServiceMethod
	client.header.Seq = seq
	client.header.Error = ""
	if err = client.cc.Write(&client.header, call.Args); err != nil {
		call = client.removeCall(seq)
		if call != nil {
			call.Error = err
			call.done()
		}
	}
}

// Go 为异步调用接口
func (client *Client) Go(serviceMethod string, args, reply interface{}, done chan *Call) *Call {
	if done == nil {
		done = make(chan *Call, 1)
	} else if cap(done) == 0 {
		log.Println("rpc client: done channel is unbuffered")
	}
	call := &Call{
		ServiceMethod: serviceMethod,
		Args:          args,
		Reply:         reply,
		Done:          done,
	}
	client.send(call)
	return call
}

func (client *Client) Call(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	call := client.Go(serviceMethod, args, reply, make(chan *Call, 1))
	select {
	case <-ctx.Done():
		client.removeCall(call.Seq)
		return errors.New("rpc client call failed: " + ctx.Err().Error())
	case call = <-call.Done:
		return call.Error
	}
}
