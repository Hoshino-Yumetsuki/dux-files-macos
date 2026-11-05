package lyrics

import "regexp"

// Per the WHATWG tag open state, "<" starts markup only before an ASCII letter, "/", "!" or "?".
var (
	markupTag  = regexp.MustCompile(`<[A-Za-z/!?][^<>]*>`)
	markupOpen = regexp.MustCompile(`<[A-Za-z/!?]`)
)

func (r *Result) withoutMarkup() *Result {
	clean := *r
	changed := false
	strip := func(text string) string {
		stripped := stripMarkup(text)
		changed = changed || stripped != text
		return stripped
	}
	clean.Attribution.Name = strip(r.Attribution.Name)
	clean.Attribution.Text = strip(r.Attribution.Text)
	clean.Plain = strip(r.Plain)
	clean.Title = strip(r.Title)
	clean.Artist = strip(r.Artist)
	clean.Album = strip(r.Album)
	if r.Synced != nil {
		clean.Synced = make([]Line, len(r.Synced))
		for i, line := range r.Synced {
			clean.Synced[i] = stripLine(line)
			changed = changed || clean.Synced[i].Text != line.Text
		}
	}
	if r.Voices != nil {
		clean.Voices = make(map[VoiceID]Voice, len(r.Voices))
		for id, voice := range r.Voices {
			voice.Name = strip(voice.Name)
			clean.Voices[id] = voice
		}
	}
	if changed {
		clean.raw = nil
	}
	return &clean
}

func stripMarkup(text string) string {
	if !markupOpen.MatchString(text) {
		return text
	}
	stripped, _ := cutMarkup([]byte(text), nil)
	return string(stripped)
}

// stripLine cuts the joined words so a tag split across words goes too.
func stripLine(line Line) Line {
	if len(line.Words) == 0 {
		line.Text = stripMarkup(line.Text)
		return line
	}
	joined := joinWords(line.Words)
	if !markupOpen.MatchString(joined) {
		return line
	}
	owners := make([]int, 0, len(joined))
	for i, word := range line.Words {
		for range len(word.Text) {
			owners = append(owners, i)
		}
	}
	text, owners := cutMarkup([]byte(joined), owners)
	words := make([]Word, 0, len(line.Words))
	for start := 0; start < len(text); {
		end := start
		for end < len(text) && owners[end] == owners[start] {
			end++
		}
		word := line.Words[owners[start]]
		word.Text = string(text[start:end])
		words = append(words, word)
		start = end
	}
	line.Words = words
	line.Text = joinWords(words)
	return line
}

// cutMarkup repeats until no cut exposes a new tag opener. owners, if set, moves with text.
func cutMarkup(text []byte, owners []int) ([]byte, []int) {
	for {
		cuts := markupTag.FindAllIndex(text, -1)
		if cuts == nil {
			cuts = markupOpen.FindAllIndex(text, -1)
			for _, cut := range cuts {
				cut[1] = cut[0] + 1
			}
		}
		if cuts == nil {
			return text, owners
		}
		text, owners = cutRanges(text, owners, cuts)
	}
}

func cutRanges(text []byte, owners []int, cuts [][]int) ([]byte, []int) {
	keptText := make([]byte, 0, len(text))
	var keptOwners []int
	from := 0
	for _, cut := range append(cuts, []int{len(text), len(text)}) {
		keptText = append(keptText, text[from:cut[0]]...)
		if owners != nil {
			keptOwners = append(keptOwners, owners[from:cut[0]]...)
		}
		from = cut[1]
	}
	return keptText, keptOwners
}
