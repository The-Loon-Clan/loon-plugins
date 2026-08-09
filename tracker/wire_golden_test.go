package tracker

import (
	"encoding/hex"
	"strings"
	"testing"
)

// The announce and scrape encoders moved from a private encoder in this
// package onto loon/bencode's Writer. These goldens are the exact bytes the
// old implementation produced, captured before the move: this is a WIRE
// format, so a drift here is not an internal detail — every BitTorrent client
// in the swarm parses it, and "it still compiles" says nothing about whether
// the bytes came out the same.
func TestWireFormatIsByteIdenticalAcrossTheEncoderMove(t *testing.T) {
	cases := []struct {
		name string
		got  []byte
		want string
	}{
		{
			"announce with ipv6",
			EncodeAnnounceResponse(1800, 5, 7, []byte{192, 168, 0, 1, 0x1a, 0xe1}, []byte{0xfe, 0x80}),
			"64383a636f6d706c65746569356531303a696e636f6d706c657465693765383a696e74657276616c69313830306531323a6d696e20696e74657276616c6939303065353a7065657273363ac0a800011ae1363a706565727336323afe8065",
		},
		{
			"announce without ipv6 omits the peers6 key entirely",
			EncodeAnnounceResponse(900, 1, 0, []byte{10, 0, 0, 1, 0x1a, 0xe1}, nil),
			"64383a636f6d706c65746569316531303a696e636f6d706c657465693065383a696e74657276616c693930306531323a6d696e20696e74657276616c6934353065353a7065657273363a0a0000011ae165",
		},
		{
			"failure reason",
			EncodeAnnounceFailure("unregistered torrent"),
			"6431343a6661696c75726520726561736f6e32303a756e7265676973746572656420746f7272656e7465",
		},
		{
			"multi-torrent scrape",
			EncodeScrapeResponse([]ScrapeEntry{
				{InfoHash: "aaaaaaaaaaaaaaaaaaaa", Complete: 3, Downloaded: 9, Incomplete: 1},
				{InfoHash: "bbbbbbbbbbbbbbbbbbbb", Complete: 0, Downloaded: 0, Incomplete: 2},
			}),
			"64353a66696c65736432303a616161616161616161616161616161616161616164383a636f6d706c65746569336531303a646f776e6c6f6164656469396531303a696e636f6d706c6574656931656532303a626262626262626262626262626262626262626264383a636f6d706c65746569306531303a646f776e6c6f6164656469306531303a696e636f6d706c657465693265656565",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hex.EncodeToString(tc.got); got != tc.want {
				t.Errorf("wire format drifted:\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}

// bencode.Writer cannot sort keys — it has no idea when a dict ends — so the
// announce response's key order is the caller's responsibility. BEP-3 requires
// bytewise order, and these keys were hand-ordered correctly; this pins that,
// so a later insertion in the wrong place fails here rather than in a client.
func TestAnnounceResponseKeysAreBytewiseSorted(t *testing.T) {
	body := string(EncodeAnnounceResponse(1800, 5, 7, []byte{1, 2, 3, 4, 0, 80}, []byte{9, 9}))
	want := []string{"complete", "incomplete", "interval", "min interval", "peers", "peers6"}
	at := make([]int, len(want))
	for i, k := range want {
		at[i] = strings.Index(body, k)
		if at[i] < 0 {
			t.Fatalf("key %q missing from the response", k)
		}
	}
	for i := 1; i < len(at); i++ {
		if at[i] < at[i-1] {
			t.Errorf("key %q precedes %q — BEP-3 requires bytewise-sorted dict keys",
				want[i], want[i-1])
		}
	}
}
