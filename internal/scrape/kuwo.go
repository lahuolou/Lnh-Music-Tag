package scrape

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// kuwoSource searches Kuwo Music (酷我音乐) via the public r.s search gateway.
type kuwoSource struct{}

func (kuwoSource) Name() string  { return "kuwo" }
func (kuwoSource) Label() string { return "酷我音乐" }

func (kuwoSource) Search(c *Client, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 || limit > 25 {
		limit = 8
	}
	u := "https://search.kuwo.cn/r.s?all=" + url.QueryEscape(query) +
		"&ft=music&itemset=web_2013&client=kt&pn=0&rn=" + strconv.Itoa(limit) +
		"&rformat=json&encoding=utf8"
	body, err := c.getBytes(u, nil)
	if err != nil {
		return nil, err
	}
	// 酷我 r.s 接口返回的是单引号 JSON（类似 Python dict），不是标准 JSON。
	// 这里把字符串外的单引号转成双引号、字符串内的双引号转义后交给标准解析器。
	fixed := singleQuoteToDouble(string(body))
	var resp struct {
		Abslist []struct {
			SONGNAME string `json:"SONGNAME"`
			ARTIST   string `json:"ARTIST"`
			ALBUM    string `json:"ALBUM"`
			ALBUMID  string `json:"ALBUMID"`
			MUSICRID string `json:"MUSICRID"`
			DURATION string `json:"DURATION"`
		} `json:"abslist"`
	}
	if err := json.Unmarshal([]byte(fixed), &resp); err != nil {
		return nil, err
	}
	out := []SearchResult{}
	for _, s := range resp.Abslist {
		id := strings.TrimPrefix(s.MUSICRID, "MUSIC_")
		secs := 0
		if n, e := strconv.Atoi(s.DURATION); e == nil {
			secs = n
		}
		out = append(out, SearchResult{
			Source: "kuwo", SourceID: id, Title: cleanKuwo(s.SONGNAME),
			Artists: splitNames(cleanKuwo(s.ARTIST)), Album: cleanKuwo(s.ALBUM),
			AlbumID: s.ALBUMID, Duration: secs,
		})
	}
	return out, nil
}

// cleanKuwo resolves legacy HTML/JSON escape leftovers such as &nbsp; and
// double-escaped ampersands (\u0026 / \\u0026 / ...) in Kuwo responses.
func cleanKuwo(s string) string {
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = unicodeAmp.ReplaceAllString(s, "&")
	return s
}

var unicodeAmp = regexp.MustCompile(`\\+u0026`)

// singleQuoteToDouble converts a single-quoted JSON-ish document (as returned
// by some domestic APIs) into standard double-quoted JSON. It tracks string
// context so apostrophes inside strings are preserved and double quotes inside
// single-quoted strings are escaped.
func singleQuoteToDouble(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	inStr := false
	quote := byte(0)
	i := 0
	for i < len(s) {
		ch := s[i]
		if !inStr {
			if ch == '\'' || ch == '"' {
				inStr = true
				quote = ch
				b.WriteByte('"')
			} else {
				b.WriteByte(ch)
			}
			i++
			continue
		}
		// inside a string
		if ch == '\\' && i+1 < len(s) {
			nxt := s[i+1]
			if quote == '\'' {
				if nxt == '\'' {
					b.WriteByte('\'') // \' -> '
				} else {
					b.WriteByte('\\')
					b.WriteByte(nxt)
				}
			} else {
				b.WriteByte('\\')
				b.WriteByte(nxt)
			}
			i += 2
			continue
		}
		if ch == quote {
			inStr = false
			b.WriteByte('"')
			i++
			continue
		}
		if quote == '\'' && ch == '"' {
			b.WriteString(`\"`) // escape inner double quotes
			i++
			continue
		}
		b.WriteByte(ch)
		i++
	}
	return b.String()
}
