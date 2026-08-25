package ethel

import "testing"

func TestSegmentShardBlockCountPartitionsRange(t *testing.T) {
	tests := []struct {
		start, end uint64
		shards     int
	}{
		{0, 10_000_000, 2},
		{24_100_000, 24_200_000, 2},
		{123, 987_654, 7},
		{HeaderSegmentSize - 3, HeaderSegmentSize + 5, 3},
	}
	for _, tt := range tests {
		var total uint64
		counts := make([]uint64, tt.shards)
		for shard := 0; shard < tt.shards; shard++ {
			counts[shard] = segmentShardBlockCount(tt.start, tt.end, tt.shards, shard)
			total += counts[shard]
		}
		if total != tt.end-tt.start {
			t.Fatalf("range %d-%d/%d totals %d, want %d (counts=%v)",
				tt.start, tt.end, tt.shards, total, tt.end-tt.start, counts)
		}
	}
}

func TestSegmentShardOwnsExactlyOneShard(t *testing.T) {
	const shards = 5
	for segment := uint64(0); segment < 100; segment++ {
		owners := 0
		for shard := 0; shard < shards; shard++ {
			if segmentShardOwns(segment, shards, shard) {
				owners++
			}
		}
		if owners != 1 {
			t.Fatalf("segment %d has %d owners, want 1", segment, owners)
		}
	}
}

func TestBodyDecoderCountTracksExecutionGroups(t *testing.T) {
	for _, tc := range []struct {
		workers int
		readers int
		want    int
	}{
		{workers: 0, readers: 3, want: 1},
		{workers: 127, readers: 3, want: 1},
		{workers: 128, readers: 3, want: 1},
		{workers: 129, readers: 3, want: 2},
		{workers: 254, readers: 8, want: 2},
		{workers: 512, readers: 3, want: 3},
	} {
		if got := bodyDecoderCount(tc.workers, tc.readers); got != tc.want {
			t.Fatalf("bodyDecoderCount(%d, %d) = %d, want %d",
				tc.workers, tc.readers, got, tc.want)
		}
	}
}
