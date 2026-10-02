package direct

import (
	"context"
	"errors"
	"testing"

	proto_downloader "github.com/n42blockchain/N42/lib/gointerfaces/downloader"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
)

type fakeDownloaderServer struct {
	proto_downloader.UnimplementedDownloaderServer
	statsReply *proto_downloader.StatsReply
	err        error
}

func (f *fakeDownloaderServer) Add(ctx context.Context, in *proto_downloader.AddRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, f.err
}
func (f *fakeDownloaderServer) ProhibitNewDownloads(ctx context.Context, in *proto_downloader.ProhibitNewDownloadsRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, f.err
}
func (f *fakeDownloaderServer) Delete(ctx context.Context, in *proto_downloader.DeleteRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, f.err
}
func (f *fakeDownloaderServer) Verify(ctx context.Context, in *proto_downloader.VerifyRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, f.err
}
func (f *fakeDownloaderServer) Stats(ctx context.Context, in *proto_downloader.StatsRequest) (*proto_downloader.StatsReply, error) {
	return f.statsReply, f.err
}

func TestDownloaderClientForwardsToServer(t *testing.T) {
	ctx := context.Background()
	fake := &fakeDownloaderServer{statsReply: &proto_downloader.StatsReply{BytesCompleted: 7}}
	c := NewDownloaderClient(fake)

	_, err := c.Add(ctx, &proto_downloader.AddRequest{})
	require.NoError(t, err)
	_, err = c.ProhibitNewDownloads(ctx, &proto_downloader.ProhibitNewDownloadsRequest{})
	require.NoError(t, err)
	_, err = c.Delete(ctx, &proto_downloader.DeleteRequest{})
	require.NoError(t, err)
	_, err = c.Verify(ctx, &proto_downloader.VerifyRequest{})
	require.NoError(t, err)
	stats, err := c.Stats(ctx, &proto_downloader.StatsRequest{})
	require.NoError(t, err)
	require.Equal(t, uint64(7), stats.BytesCompleted)

	fake.err = errors.New("boom")
	_, err = c.Add(ctx, &proto_downloader.AddRequest{})
	require.Error(t, err)
}
