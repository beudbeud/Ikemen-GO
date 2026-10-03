package main

import (
	"bytes"
	"testing"
)

func TestReadPalette(t *testing.T) {
	s := newSff()
	s.header.Version = [4]byte{2, 0, 0, 0} // v2.0.0.0: alpha forced
	raw := []byte{1, 2, 3, 9, 4, 5, 6, 9, 7, 8, 9, 9}
	f := bytes.NewReader(append([]byte{0xee, 0xee}, raw...))
	pal, err := s.ReadPalette(f, 2, uint32(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	want := []uint32{0x00030201, 0xff060504, 0xff090807}
	if len(pal) != 16 {
		t.Fatalf("len %d, want 16 (padded)", len(pal))
	}
	for i, w := range want {
		if pal[i] != w {
			t.Errorf("colour %d = %#x, want %#x", i, pal[i], w)
		}
	}
	if pal[3] != 0xff000000 {
		t.Errorf("padding colour = %#x, want opaque black", pal[3])
	}
}
