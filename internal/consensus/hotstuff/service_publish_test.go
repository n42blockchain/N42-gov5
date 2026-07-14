package hotstuff

import "testing"

func TestTimeoutPublishPeerTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		quorum int
		want   int
	}{
		{name: "single validator", quorum: 1, want: 0},
		{name: "four validators", quorum: 3, want: 2},
		{name: "seven validators", quorum: 5, want: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := timeoutPublishPeerTarget(tt.quorum); got != tt.want {
				t.Fatalf("timeoutPublishPeerTarget(%d) = %d, want %d", tt.quorum, got, tt.want)
			}
		})
	}
}
