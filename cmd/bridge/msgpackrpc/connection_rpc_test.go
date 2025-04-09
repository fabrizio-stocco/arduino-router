package msgpackrpc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/vmihailenco/msgpack/v5"
	"go.bug.st/f"
)

func TestRPCConnection(t *testing.T) {
	testdataIn := bytes.NewBuffer(nil)
	testdataIn.Write(f.Must(msgpack.Marshal([]any{messageTypeNotification, "initialized", []any{123}})))
	testdataIn.Write(f.Must(msgpack.Marshal([]any{messageTypeRequest, MessageID(1), "textDocument/didOpen", []any{}})))
	testdataIn.Write(f.Must(msgpack.Marshal([]any{messageTypeRequest, MessageID(2), "textDocument/didClose", []any{}})))
	testdataIn.Write(f.Must(msgpack.Marshal([]any{messageTypeRequest, MessageID(3), "tocancel", []any{}})))
	testdataIn.Write(f.Must(msgpack.Marshal([]any{messageTypeNotification, "$/cancelRequest", []any{MessageID(3)}})))
	testdataIn.Write(f.Must(msgpack.Marshal([]any{messageTypeResponse, MessageID(1), nil, map[string]any{"fakedata": uint32(999)}})))

	testdataOut := bytes.NewBuffer(nil)

	type CustomError struct {
		Code    int
		Message string
	}

	resp := ""
	var wg sync.WaitGroup
	conn := NewConnection(
		testdataIn,
		testdataOut,
		func(ctx context.Context, logger FunctionLogger, method string, params []any, respCallback func(result any, err any)) {
			log := fmt.Sprintf("REQ method=%v params=%v", method, params)
			t.Log(log)
			resp += log + "\n"
			if method == "tocancel" {
				wg.Add(1)
				go func() {
					select {
					case <-ctx.Done():
						t.Log("Request has been correclty canceled")
					case <-time.After(time.Second):
						t.Log("Request has not been canceled!")
						t.Fail()
					}
					respCallback(nil, CustomError{Code: 1, Message: "error message"})
					wg.Done()
				}()
				return
			}
			respCallback([]any{}, nil)
		},
		func(logger FunctionLogger, method string, params []any) {
			log := fmt.Sprintf("NOT method=%v params=%v", method, params)
			t.Log(log)
			resp += log + "\n"
		},
		func(e error) {
			if e == io.EOF {
				return
			}
			resp += fmt.Sprintf("error=%s\n", e)
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		respRes, respErr, err := conn.SendRequest(ctx, "helloworld", []any{true})
		require.NoError(t, err)
		require.Nil(t, respErr)
		require.Equal(t, map[string]any{"fakedata": uint32(999)}, respRes)
	}()
	cancel()
	time.Sleep(100 * time.Millisecond)
	conn.Run() // Exits when input is fully consumed
	wg.Wait()  // Wait for all pending responses to get through
	conn.Close()
	require.Equal(t,
		"NOT method=initialized params=[123]\n"+
			"REQ method=textDocument/didOpen params=[]\n"+
			"REQ method=textDocument/didClose params=[]\n"+
			"REQ method=tocancel params=[]\n"+
			"", resp)

	expectedOut := bytes.NewBuffer(nil)
	expectedOut.Write(f.Must(msgpack.Marshal([]any{messageTypeRequest, MessageID(1), "helloworld", []any{true}})))
	expectedOut.Write(f.Must(msgpack.Marshal([]any{messageTypeNotification, "$/cancelRequest", []any{MessageID(1)}})))
	expectedOut.Write(f.Must(msgpack.Marshal([]any{messageTypeResponse, MessageID(1), nil, []any{}})))
	expectedOut.Write(f.Must(msgpack.Marshal([]any{messageTypeResponse, MessageID(2), nil, []any{}})))
	expectedOut.Write(f.Must(msgpack.Marshal([]any{messageTypeResponse, MessageID(3), CustomError{Code: 1, Message: "error message"}, nil})))
	// d := msgpack.NewDecoder(testdataOut) // Uncomment in case of mismatched output to debug
	// fmt.Println(d.DecodeSlice())
	// fmt.Println(d.DecodeSlice())
	// fmt.Println(d.DecodeSlice())
	// fmt.Println(d.DecodeSlice())
	// fmt.Println(d.DecodeSlice())
	require.Equal(t, expectedOut.Bytes(), testdataOut.Bytes())
}
