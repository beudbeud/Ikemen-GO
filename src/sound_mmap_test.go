//go:build unix

package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// A wave read from a mapped .snd must decode exactly like the heap copy did.
func TestSndMmap(t *testing.T) {
	var pcm bytes.Buffer
	for i := 0; i < 200; i++ {
		binary.Write(&pcm, binary.LittleEndian, int16(i*100))
	}
	var wav bytes.Buffer
	w := func(v interface{}) { binary.Write(&wav, binary.LittleEndian, v) }
	wav.WriteString("RIFF")
	w(uint32(36 + pcm.Len()))
	wav.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1))     // PCM
	w(uint16(1))     // mono
	w(uint32(8000))  // sample rate
	w(uint32(16000)) // byte rate
	w(uint16(2))     // block align
	w(uint16(16))    // bits per sample
	wav.WriteString("data")
	w(uint32(pcm.Len()))
	wav.Write(pcm.Bytes())

	var snd bytes.Buffer
	s := func(v interface{}) { binary.Write(&snd, binary.LittleEndian, v) }
	snd.WriteString("ElecbyteSnd\x00")
	s(uint16(0))
	s(uint16(1))
	s(uint32(1))  // one sound
	s(uint32(24)) // its subheader follows the header
	s(uint32(0))  // next subheader
	s(uint32(wav.Len()))
	s([2]int32{1, 2})
	snd.Write(wav.Bytes())
	path := filepath.Join(t.TempDir(), "t.snd")
	if err := os.WriteFile(path, snd.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}

	sn, err := LoadSndFiltered(path, func([2]int32) bool { return true }, 0)
	if err != nil {
		t.Fatal(err)
	}
	so := sn.Get([2]int32{1, 2})
	if so == nil || so.mapping == nil {
		t.Fatalf("sound not read from the mapping: %+v", so)
	}
	if !bytes.Equal(so.wavData, wav.Bytes()) {
		t.Fatal("mapped wave differs from the file's")
	}
	var buf [256][2]float64
	n, _ := so.GetStreamer().Stream(buf[:])
	if n != 200 || buf[1][0] == 0 || buf[1][0] != buf[1][1] {
		t.Fatalf("decoded %d samples, sample 1 = %v", n, buf[1])
	}
}
