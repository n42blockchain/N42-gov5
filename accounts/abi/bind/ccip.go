package bind

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	N42 "github.com/n42blockchain/N42"
	"github.com/n42blockchain/N42/accounts/abi"
	"github.com/n42blockchain/N42/common/hexutil"
	rpcjson "github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

const defaultCCIPReadMaxRedirects = 4

type ccipGatewayRequest struct {
	Data   string `json:"data"`
	Sender string `json:"sender"`
}

type ccipGatewayResponse struct {
	Data    string `json:"data"`
	Message string `json:"message,omitempty"`
}

func (c *BoundContract) call(opts *CallOpts, input []byte) ([]byte, error) {
	msg := N42.CallMsg{From: opts.From, To: &c.address, Data: input}
	ctx := ensureContext(opts.Context)
	maxRedirects := opts.CCIPReadMaxRedirects
	if maxRedirects <= 0 {
		maxRedirects = defaultCCIPReadMaxRedirects
	}
	for redirects := 0; ; redirects++ {
		output, err := c.callOnce(ctx, opts, msg)
		if err == nil {
			return output, nil
		}
		if !opts.EnableCCIPRead {
			return nil, err
		}
		if redirects >= maxRedirects {
			return nil, fmt.Errorf("ccip-read exceeded maximum redirects (%d): %w", maxRedirects, err)
		}
		lookup, ok, lookupErr := unpackOffchainLookupError(err)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if !ok {
			return nil, err
		}
		if lookup.Sender != c.address {
			return nil, fmt.Errorf("ccip-read sender mismatch: got %s want %s", lookup.Sender.Hex(), c.address.Hex())
		}
		response, err := resolveOffchainLookup(ctx, opts.ccipReadClient(), lookup)
		if err != nil {
			return nil, err
		}
		callbackArgs, err := abi.PackOffchainLookupResponse(response, lookup.ExtraData)
		if err != nil {
			return nil, err
		}
		callbackData := make([]byte, 4+len(callbackArgs))
		copy(callbackData[:4], lookup.CallbackFunction[:])
		copy(callbackData[4:], callbackArgs)
		msg = N42.CallMsg{From: opts.From, To: &c.address, Data: callbackData}
	}
}

func (c *BoundContract) callOnce(ctx context.Context, opts *CallOpts, msg N42.CallMsg) ([]byte, error) {
	if opts.Pending {
		pb, ok := c.caller.(PendingContractCaller)
		if !ok {
			return nil, ErrNoPendingState
		}
		return pb.PendingCallContract(ctx, msg)
	}
	return c.caller.CallContract(ctx, msg, opts.BlockNumber)
}

func (opts *CallOpts) ccipReadClient() *http.Client {
	if opts != nil && opts.CCIPReadClient != nil {
		return opts.CCIPReadClient
	}
	return http.DefaultClient
}

func unpackOffchainLookupError(err error) (*abi.OffchainLookup, bool, error) {
	raw, ok := revertErrorData(err)
	if !ok || !abi.IsOffchainLookup(raw) {
		return nil, false, nil
	}
	lookup, unpackErr := abi.UnpackOffchainLookup(raw)
	if unpackErr != nil {
		return nil, false, unpackErr
	}
	return lookup, true, nil
}

func revertErrorData(err error) ([]byte, bool) {
	var rpcErr rpcjson.Error
	var dataErr rpcjson.DataError
	if errors.As(err, &rpcErr) && errors.As(err, &dataErr) && rpcErr.ErrorCode() == 3 {
		encoded, ok := dataErr.ErrorData().(string)
		if !ok {
			return nil, false
		}
		revertData, decodeErr := hexutil.Decode(encoded)
		if decodeErr == nil {
			return revertData, true
		}
	}
	return nil, false
}

func resolveOffchainLookup(ctx context.Context, client *http.Client, lookup *abi.OffchainLookup) ([]byte, error) {
	if len(lookup.URLs) == 0 {
		return nil, errors.New("ccip-read gateway list is empty")
	}
	sender := strings.ToLower(lookup.Sender.Hex())
	calldata := hexutil.Encode(lookup.CallData)
	var lastErr error
	for _, rawURL := range lookup.URLs {
		url := strings.ReplaceAll(rawURL, "{sender}", sender)
		var (
			response []byte
			err      error
		)
		if strings.Contains(url, "{data}") {
			url = strings.ReplaceAll(url, "{data}", calldata)
			response, err = ccipReadGET(ctx, client, url)
		} else {
			response, err = ccipReadPOST(ctx, client, url, sender, calldata)
		}
		if err == nil {
			return response, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("ccip-read did not produce a gateway request")
	}
	return nil, lastErr
}

func ccipReadGET(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	return parseGatewayResponse(resp)
}

func ccipReadPOST(ctx context.Context, client *http.Client, url string, sender string, calldata string) ([]byte, error) {
	payload, err := json.Marshal(ccipGatewayRequest{
		Data:   calldata,
		Sender: sender,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	return parseGatewayResponse(resp)
}

func parseGatewayResponse(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		return nil, fmt.Errorf("ccip-read gateway request failed: %s", message)
	}

	var payload ccipGatewayResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("ccip-read gateway returned invalid JSON: %w", err)
	}
	if payload.Data == "" {
		if payload.Message != "" {
			return nil, errors.New(payload.Message)
		}
		return nil, errors.New("ccip-read gateway response missing data")
	}
	data, err := hexutil.Decode(payload.Data)
	if err != nil {
		return nil, fmt.Errorf("ccip-read gateway returned invalid hex data: %w", err)
	}
	return data, nil
}
