package scrape

// musicbrainzSource adapts the existing MusicBrainz recording search into the
// Source interface. Cover art comes from the Cover Art Archive via ReleaseMBID.
type musicbrainzSource struct{}

func (musicbrainzSource) Name() string  { return "musicbrainz" }
func (musicbrainzSource) Label() string { return "MusicBrainz" }

func (musicbrainzSource) Search(c *Client, query string, limit int) ([]SearchResult, error) {
	recs, err := c.SearchRecordings(query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(recs))
	for _, r := range recs {
		sr := SearchResult{
			Source: "musicbrainz", SourceID: r.MBID,
			Title: r.Title, Artists: r.Artists,
			Duration: r.Duration / 1000, Date: r.FirstDate,
		}
		if len(r.Releases) > 0 {
			sr.Album = r.Releases[0].Title
			sr.ReleaseMBID = r.Releases[0].MBID
			if sr.Date == "" {
				sr.Date = r.Releases[0].Date
			}
		}
		out = append(out, sr)
	}
	return out, nil
}
