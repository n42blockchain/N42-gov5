package sync

import (
	"bytes"
	"errors"
	"fmt"

	libp2pcore "github.com/libp2p/go-libp2p/core"
	"github.com/libp2p/go-libp2p/core/network"

	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/p2p/encoder"
	"github.com/n42blockchain/N42/internal/p2p/p2ptypes"
	"github.com/n42blockchain/N42/log"
)

var (
	responseCodeSuccess        = byte(0x00)
	responseCodeInvalidRequest = byte(0x01)
	responseCodeServerError    = byte(0x02)
)

func (s *Service) generateErrorResponse(code byte, reason string) ([]byte, error) {
	return createErrorResponse(code, reason, s.cfg.p2p)
}

// ReadStatusCode reads the response status code from a RPC stream.
func ReadStatusCode(stream network.Stream, encoding encoder.NetworkEncoding) (uint8, string, error) {
	SetStreamReadDeadline(stream, ttfbTimeout)
	b := make([]byte, 1)
	if _, err := stream.Read(b); err != nil {
		return 0, "", err
	}

	if b[0] == responseCodeSuccess {
		SetStreamReadDeadline(stream, respTimeout)
		return 0, "", nil
	}

	// Non-success: read the error message.
	SetStreamReadDeadline(stream, respTimeout)
	msg := &p2ptypes.ErrorMessage{}
	if err := encoding.DecodeWithMaxLength(stream, msg); err != nil {
		return 0, "", err
	}
	return b[0], string(*msg), nil
}

func writeErrorResponseToStream(responseCode byte, reason string, stream libp2pcore.Stream, encoder p2p.EncodingProvider) {
	resp, err := createErrorResponse(responseCode, reason, encoder)
	if err != nil {
		log.Debug("Could not generate a response error", "err", err)
	} else if _, err := stream.Write(resp); err != nil {
		log.Debug("Could not write to stream", "err", err)
	} else {
		closeStream(stream)
	}
}

func createErrorResponse(code byte, reason string, encoder p2p.EncodingProvider) ([]byte, error) {
	buf := bytes.NewBuffer([]byte{code})
	errMsg := p2ptypes.ErrorMessage(reason)
	if _, err := encoder.Encoding().EncodeWithMaxLength(buf, &errMsg); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// isValidStreamError returns true for errors that are not stream resets.
func isValidStreamError(err error) bool {
	return err != nil && !errors.Is(err, network.ErrReset) && err.Error() != network.ErrReset.Error()
}

func closeStream(stream network.Stream) {
	if err := stream.Close(); isValidStreamError(err) {
		log.Debug(fmt.Sprintf("Could not close stream with protocol %s", stream.Protocol()), "err", err)
	}
}

func closeStreamAndWait(stream network.Stream) {
	if err := stream.CloseWrite(); err != nil {
		_ = stream.Reset()
		if isValidStreamError(err) {
			log.Debug(fmt.Sprintf("Could not close-write stream with protocol %s", stream.Protocol()), "err", err)
		}
		return
	}
	// Wait for the remote side to acknowledge by reading until EOF or error.
	// We don't inspect the result -- we just need to wait before closing.
	_, _ = stream.Read([]byte{0})
	_ = stream.Close()
}
