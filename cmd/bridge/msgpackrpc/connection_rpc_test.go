package msgpackrpc

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/djherbis/buffer"
	"github.com/djherbis/nio/v3"
	"github.com/stretchr/testify/require"
	"github.com/vmihailenco/msgpack/v5"
	"go.bug.st/f"
)

func TestRPCConnection(t *testing.T) {
	in, testdataIn := nio.Pipe(buffer.New(1024))
	testdataOut, out := nio.Pipe(buffer.New(1024))
	d := msgpack.NewDecoder(testdataOut)

	type CustomError struct {
		Code    int
		Message string
	}

	var wg sync.WaitGroup
	notification := ""
	request := ""
	requestError := ""
	conn := NewConnection(
		in, out,
		func(ctx context.Context, logger FunctionLogger, method string, params []any) (_result any, _err any) {
			defer wg.Done()
			request = fmt.Sprintf("REQ method=%v params=%v", method, params)
			if method == "tocancel" {
				select {
				case <-ctx.Done():
					request += " canceled"
				case <-time.After(time.Second):
					request += " not canceled"
					t.Fail()
				}
				return nil, CustomError{Code: 1, Message: "error message"}
			}
			return []any{}, nil
		},
		func(logger FunctionLogger, method string, params []any) {
			defer wg.Done()
			notification = fmt.Sprintf("NOT method=%v params=%v", method, params)
		},
		func(e error) {
			defer wg.Done()
			t.Log(e)
			if e == io.EOF {
				return
			}
			requestError = fmt.Sprintf("error=%s", e)
		},
	)
	t.Cleanup(func() {
		wg.Add(1) // this will produce an error in the callback handler
		conn.Close()
	})
	go conn.Run()

	send := func(msg ...any) {
		testdataIn.Write(f.Must(msgpack.Marshal(msg)))
	}
	sendCancel := func(id MessageID) {
		send(messageTypeNotification, "$/cancelRequest", []any{id})
	}

	{ // Test incoming notification
		wg.Add(1)
		send(messageTypeNotification, "initialized", []any{123})
		wg.Wait()
		require.Equal(t, "NOT method=initialized params=[123]", notification)
	}

	{ // Test incoming request
		wg.Add(1)
		send(messageTypeRequest, MessageID(1), "textDocument/didOpen", []any{})
		wg.Wait()
		require.Equal(t, "REQ method=textDocument/didOpen params=[]", request)
		msg, err := d.DecodeSlice()
		require.NoError(t, err)
		require.Equal(t, []any{int8(1), uint32(1), nil, []any{}}, msg)
	}

	{ // Test another incoming request
		wg.Add(1)
		send(messageTypeRequest, MessageID(2), "textDocument/didClose", []any{})
		wg.Wait()
		require.Equal(t, "REQ method=textDocument/didClose params=[]", request)
		msg, err := d.DecodeSlice()
		require.NoError(t, err)
		require.Equal(t, []any{int8(1), uint32(2), nil, []any{}}, msg)
	}

	{ // Test incoming request cancelation
		wg.Add(1)
		send(messageTypeRequest, MessageID(3), "tocancel", []any{})
		time.Sleep(time.Millisecond * 100)
		sendCancel(3)
		wg.Wait()
		require.Equal(t, "REQ method=tocancel params=[] canceled", request)
		msg, err := d.DecodeSlice()
		require.NoError(t, err)
		require.Equal(t, []any{int8(1), uint32(3), map[string]any{"Code": int8(1), "Message": "error message"}, nil}, msg)
	}

	{ // Test outgoing request
		wg.Add(1)
		go func() {
			defer wg.Done()
			respRes, respErr, err := conn.SendRequest(t.Context(), "helloworld", []any{true})
			require.NoError(t, err)
			require.Nil(t, respErr)
			require.Equal(t, map[string]any{"fakedata": uint32(999)}, respRes)
		}()
		msg, err := d.DecodeSlice() // Grab the SendRequest
		require.NoError(t, err)
		require.Equal(t, []any{int8(0), uint32(1), "helloworld", []any{true}}, msg)
		send(messageTypeResponse, MessageID(1), nil, map[string]any{"fakedata": uint32(999)})
		wg.Wait()
	}

	{ // Test invalid response
		wg.Add(1)
		send(int8(1), MessageID(999), 10, nil)
		wg.Wait()
		require.Equal(t, "error=invalid ID in request response '999': double answer or request not sent", requestError)
	}
}
