package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRenameCleanFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, magic []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, append(magic, []byte("dummy audio data")...), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	cases := []struct {
		name  string
		magic []byte
		want  string // expected final base name; "" means unchanged
	}{
		// 发如雪.mp3.flac but actually FLAC -> 发如雪.flac
		{"发如雪.mp3.flac", []byte("fLaC"), "发如雪.flac"},
		// 发如雪.mp3.mp3 but actually MP3 -> 发如雪.mp3
		{"发如雪.mp3.mp3", []byte("ID3"), "发如雪.mp3"},
		// already-clean single extension matching format -> unchanged
		{"ok.mp3", []byte("ID3"), ""},
		// extension mismatches real encoding -> rename to real one
		{"song.flac", []byte("ID3"), "song.mp3"},
		// m4a ftyp
		{"t.m4a.mp4", []byte{0, 0, 0, 24, 'f', 't', 'y', 'p'}, "t.m4a"},
	}

	for _, c := range cases {
		p := write(c.name, c.magic)
		np, err := renameCleanFile(p)
		if err != nil {
			t.Fatalf("%s: rename error: %v", c.name, err)
		}
		base := filepath.Base(np)
		if c.want == "" {
			if base != c.name {
				t.Errorf("%s: expected unchanged, got %s", c.name, base)
			}
		} else if base != c.want {
			t.Errorf("%s: expected %s, got %s", c.name, c.want, base)
		}
	}
}
