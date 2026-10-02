package sync

import (
	"context"
	"testing"

	fastssz "github.com/prysmaticlabs/fastssz"

	"github.com/n42blockchain/N42/proto/sync_pb"
)

// syncTWriteSuccess writes a success response code followed by the SSZ
// encoding of msg to the stream, mirroring what a real RPC handler does.
func syncTWriteSuccess(t *testing.T, fp *fakeP2P, stream *fakeStream, msg fastssz.Marshaler) {
	t.Helper()
	if _, err := stream.Write([]byte{responseCodeSuccess}); err != nil {
		t.Fatalf("write success code: %v", err)
	}
	if _, err := fp.Encoding().EncodeWithMaxLength(stream, msg); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func TestSendBlobSidecarsByRangeSuccess(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	resp := &sync_pb.BlobSidecarsResponse{}
	go func() {
		defer server.Close()
		syncTWriteSuccess(t, fp, server, resp)
	}()
	got, err := SendBlobSidecarsByRange(context.Background(), fp, fp.self, &sync_pb.BlobSidecarsByRangeRequest{})
	if err != nil {
		t.Fatalf("SendBlobSidecarsByRange: %v", err)
	}
	if got == nil {
		t.Fatal("expected a non-nil response")
	}
}

func TestSendBlobSidecarsByRangeErrorCode(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	go func() {
		defer server.Close()
		writeErrorResponseToStream(responseCodeServerError, "nope", server, fp)
	}()
	_, err := SendBlobSidecarsByRange(context.Background(), fp, fp.self, &sync_pb.BlobSidecarsByRangeRequest{})
	if err == nil {
		t.Fatal("expected an error for a server-error response code")
	}
}

func TestSendBlobSidecarsByRangeSendError(t *testing.T) {
	fp := newFakeP2P(t)
	fp.sendErr = errBoom
	_, err := SendBlobSidecarsByRange(context.Background(), fp, fp.self, &sync_pb.BlobSidecarsByRangeRequest{})
	if err == nil {
		t.Fatal("expected error when Send fails")
	}
}

func TestSendBlobSidecarsByRootSuccess(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	resp := &sync_pb.BlobSidecarsResponse{}
	go func() {
		defer server.Close()
		syncTWriteSuccess(t, fp, server, resp)
	}()
	got, err := SendBlobSidecarsByRoot(context.Background(), fp, fp.self, &sync_pb.BlobSidecarsByRootRequest{})
	if err != nil {
		t.Fatalf("SendBlobSidecarsByRoot: %v", err)
	}
	if got == nil {
		t.Fatal("expected a non-nil response")
	}
}

func TestSendBlobSidecarsByRootErrorCode(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	go func() {
		defer server.Close()
		writeErrorResponseToStream(responseCodeInvalidRequest, "bad", server, fp)
	}()
	_, err := SendBlobSidecarsByRoot(context.Background(), fp, fp.self, &sync_pb.BlobSidecarsByRootRequest{})
	if err == nil {
		t.Fatal("expected an error for an invalid-request response code")
	}
}

func TestSendGetAccountRangeSuccess(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	resp := &sync_pb.AccountRangeResponse{Completed: true}
	go func() {
		defer server.Close()
		syncTWriteSuccess(t, fp, server, resp)
	}()
	got, err := SendGetAccountRange(context.Background(), fp, fp.self, &sync_pb.GetAccountRangeRequest{})
	if err != nil {
		t.Fatalf("SendGetAccountRange: %v", err)
	}
	if !got.Completed {
		t.Fatal("expected Completed = true to round-trip")
	}
}

func TestSendGetAccountRangeErrorCode(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	go func() {
		defer server.Close()
		writeErrorResponseToStream(responseCodeServerError, "nope", server, fp)
	}()
	_, err := SendGetAccountRange(context.Background(), fp, fp.self, &sync_pb.GetAccountRangeRequest{})
	if err == nil {
		t.Fatal("expected an error for a server-error response code")
	}
}

func TestSendGetAccountRangeSendError(t *testing.T) {
	fp := newFakeP2P(t)
	fp.sendErr = errBoom
	_, err := SendGetAccountRange(context.Background(), fp, fp.self, &sync_pb.GetAccountRangeRequest{})
	if err == nil {
		t.Fatal("expected error when Send fails")
	}
}

func TestSendGetStorageRangeSuccess(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	resp := &sync_pb.StorageRangeResponse{Completed: true}
	go func() {
		defer server.Close()
		syncTWriteSuccess(t, fp, server, resp)
	}()
	got, err := SendGetStorageRange(context.Background(), fp, fp.self, &sync_pb.GetStorageRangeRequest{})
	if err != nil {
		t.Fatalf("SendGetStorageRange: %v", err)
	}
	if !got.Completed {
		t.Fatal("expected Completed = true to round-trip")
	}
}

func TestSendGetStorageRangeErrorCode(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	go func() {
		defer server.Close()
		writeErrorResponseToStream(responseCodeServerError, "nope", server, fp)
	}()
	_, err := SendGetStorageRange(context.Background(), fp, fp.self, &sync_pb.GetStorageRangeRequest{})
	if err == nil {
		t.Fatal("expected an error for a server-error response code")
	}
}

func TestSendGetCodeSuccess(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	resp := &sync_pb.CodeResponse{}
	go func() {
		defer server.Close()
		syncTWriteSuccess(t, fp, server, resp)
	}()
	got, err := SendGetCode(context.Background(), fp, fp.self, &sync_pb.GetCodeRequest{})
	if err != nil {
		t.Fatalf("SendGetCode: %v", err)
	}
	if got == nil {
		t.Fatal("expected a non-nil response")
	}
}

func TestSendGetCodeErrorCode(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	go func() {
		defer server.Close()
		writeErrorResponseToStream(responseCodeServerError, "nope", server, fp)
	}()
	_, err := SendGetCode(context.Background(), fp, fp.self, &sync_pb.GetCodeRequest{})
	if err == nil {
		t.Fatal("expected an error for a server-error response code")
	}
}

func TestSendGetCodeSendError(t *testing.T) {
	fp := newFakeP2P(t)
	fp.sendErr = errBoom
	_, err := SendGetCode(context.Background(), fp, fp.self, &sync_pb.GetCodeRequest{})
	if err == nil {
		t.Fatal("expected error when Send fails")
	}
}

