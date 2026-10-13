package main

import (
	"testing"

	"LNH-musictag/internal/model"
	"LNH-musictag/internal/taglibx"
)

func TestFieldsFromList(t *testing.T) {
	f := fieldsFromList([]string{"cover", "歌名", "流派"}, false, false)
	if !f.Cover || !f.Title || !f.Genre {
		t.Fatalf("expected cover/title/genre, got %+v", f)
	}
	if f.Artist || f.Lyrics || f.Year || f.AlbumArtist || f.TrackNumber || f.Album {
		t.Fatalf("unexpected fields enabled: %+v", f)
	}
	// empty list -> all metadata + legacy fetch flags
	f2 := fieldsFromList(nil, true, true)
	if !f2.Title || !f2.Artist || !f2.Album || !f2.Cover || !f2.Lyrics || !f2.Genre || !f2.Year || !f2.AlbumArtist || !f2.TrackNumber {
		t.Fatalf("empty list should default to all, got %+v", f2)
	}
	// empty list + no fetch -> all metadata but no cover/lyrics
	f3 := fieldsFromList(nil, false, false)
	if f3.Cover || f3.Lyrics {
		t.Fatalf("no cover/lyrics when fetch flags off: %+v", f3)
	}
}

func TestApplySkipFilled(t *testing.T) {
	full := &model.Track{Tags: map[string]string{
		taglibx.Title: "x", taglibx.Artist: "x", taglibx.Album: "x",
		taglibx.AlbumArtist: "x", taglibx.Genre: "x", taglibx.Date: "2020",
		taglibx.TrackNumber: "1",
		taglibx.Composer:    "x", taglibx.Lyricist: "x", taglibx.ReleaseType: "x",
		taglibx.Language: "x", taglibx.Comment: "x", taglibx.DiscNumber: "1",
		taglibx.DiscTotal: "1", taglibx.TrackTotal: "1",
		taglibx.ReplayGainTrackGain: "1", taglibx.ReplayGainAlbumGain: "1",
		taglibx.ReplayGainTrackPeak: "1", taglibx.ReplayGainAlbumPeak: "1",
		taglibx.Bitrate: "320", taglibx.SampleRate: "44100",
		taglibx.BitDepth: "16", taglibx.Channels: "2",
		taglibx.Length: "300", taglibx.FileSize: "12345",
	}}
	full.HasCover = true
	// all metadata filled + cover -> nothing to do
	f := allMetadataFields()
	f.Cover = true
	if applySkipFilled(full, &f) {
		t.Fatal("all filled should return no work")
	}
	// cover missing -> work needed and cover stays enabled
	full.HasCover = false
	f2 := ScrapeFields{Cover: true, Title: true, Artist: true}
	if !applySkipFilled(full, &f2) {
		t.Fatal("cover missing should need work")
	}
	if !f2.Cover {
		t.Fatal("cover should remain enabled")
	}
	if f2.Title || f2.Artist {
		t.Fatal("filled title/artist should be dropped")
	}
	// lyrics missing -> work needed
	f3 := ScrapeFields{Lyrics: true}
	if !applySkipFilled(full, &f3) {
		t.Fatal("lyrics missing should need work")
	}
}
