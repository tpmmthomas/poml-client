// Package rpc exposes operator actions over a local JSON-RPC HTTP endpoint.
package rpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/thomas/poml-node/internal/codec"
	"github.com/thomas/poml-node/internal/types"
)

type Service struct {
	Address     string
	Token       string
	NodeInfo    func() map[string]any
	Tip         func() (types.Hash, uint64)
	Block       func(types.Hash) (types.Block, error)
	SubmitTx    func(types.Tx) error
	StartMining func() error
	StopMining  func()
	EventsPath  string
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   any    `json:"error,omitempty"`
}

func (s Service) Serve(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.Address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: s.handler()}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	return server.Serve(listener)
}

func (s Service) handler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, requestHTTP *http.Request) {
		if s.Token != "" && requestHTTP.Header.Get("Authorization") != "Bearer "+s.Token {
			writeResponse(writer, response{JSONRPC: "2.0", Error: "unauthorized"})
			return
		}
		if requestHTTP.Method != http.MethodPost || requestHTTP.URL.Path != "/rpc" {
			writeResponse(writer, response{JSONRPC: "2.0", Error: "POST /rpc is required"})
			return
		}
		requestHTTP.Body = http.MaxBytesReader(writer, requestHTTP.Body, 2<<20)
		var input request
		if err := json.NewDecoder(requestHTTP.Body).Decode(&input); err != nil {
			writeResponse(writer, response{JSONRPC: "2.0", ID: input.ID, Error: err.Error()})
			return
		}
		result, err := s.call(input.Method, input.Params)
		if err != nil {
			writeResponse(writer, response{JSONRPC: "2.0", ID: input.ID, Error: err.Error()})
			return
		}
		writeResponse(writer, response{JSONRPC: "2.0", ID: input.ID, Result: result})
	})
}

func (s Service) call(method string, params json.RawMessage) (any, error) {
	switch method {
	case "get_node_info":
		return s.NodeInfo(), nil
	case "get_tip":
		tip, height := s.Tip()
		return map[string]any{"height": height, "hash": tip.String()}, nil
	case "get_block":
		var input struct {
			Hash string `json:"hash"`
		}
		if err := json.Unmarshal(params, &input); err != nil {
			return nil, err
		}
		hash, err := types.ParseHash(input.Hash)
		if err != nil {
			return nil, err
		}
		block, err := s.Block(hash)
		if err != nil {
			return nil, err
		}
		encoded := base64.StdEncoding.EncodeToString(codec.BlockBytes(block))
		return map[string]any{"hash": hash.String(), "block_base64": encoded, "height": block.Header.Height}, nil
	case "submit_transaction", "submit_query":
		var input struct {
			TransactionBase64 string `json:"transaction_base64"`
		}
		if err := json.Unmarshal(params, &input); err != nil {
			return nil, err
		}
		data, err := base64.StdEncoding.DecodeString(input.TransactionBase64)
		if err != nil {
			return nil, err
		}
		tx, err := codec.DecodeTx(data)
		if err != nil {
			return nil, err
		}
		if err := s.SubmitTx(tx); err != nil {
			return nil, err
		}
		return map[string]any{"tx_id": codec.TxID(tx).String()}, nil
	case "start_mining":
		if s.StartMining == nil {
			return nil, errors.New("mining is unavailable")
		}
		return nil, s.StartMining()
	case "stop_mining":
		if s.StopMining != nil {
			s.StopMining()
		}
		return map[string]any{"stopped": true}, nil
	case "get_events":
		return map[string]any{"path": s.EventsPath}, nil
	default:
		return nil, fmt.Errorf("unknown method %q", method)
	}
}

func writeResponse(writer http.ResponseWriter, value response) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

func Call(ctx context.Context, address, token, method string, params any) (json.RawMessage, error) {
	body, err := json.Marshal(request{JSONRPC: "2.0", ID: 1, Method: method, Params: mustJSON(params)})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/rpc", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var decoded struct {
		Result json.RawMessage `json:"result"`
		Error  any             `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if decoded.Error != nil {
		return nil, fmt.Errorf("RPC error: %v", decoded.Error)
	}
	return decoded.Result, nil
}

func mustJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}
