package usenet

import "testing"

func subjArt(subject string) stagedArticle { return stagedArticle{Subject: subject} }

// What a set IS, read from its article filenames. This is the signal the
// release title often does not carry: `Erotic Magazine - Fiesta Readers Wives
// 23` announces nothing, and was deleted by a size rule for being 2.2 MB.
func TestContentKindFromArticles(t *testing.T) {
	cases := []struct {
		name string
		subs []string
		want string
	}{
		{"ebook", []string{`[1/2] - "Blackthorn - J.T. Geissinger.epub" yEnc (1/3)`}, kindBook},
		{"magazine scan", []string{`"Fiesta Readers Wives 23.pdf" yEnc (1/9)`}, kindBook},
		{"comics keep their own kind", []string{`"Chapter 214.cbz" yEnc (1/4)`}, kindComics},
		{"lossless audio", []string{`"01 - Track.flac" yEnc (1/7)`}, kindAudio},
		{"video", []string{`"Show.S01E01.1080p.mkv" yEnc (1/900)`}, kindVideo},
		{"installer", []string{`"Setup.msi" yEnc (1/40)`}, kindSoftware},
		{"image set", []string{`"img001.jpg" yEnc (1/2)`}, kindImage},
		{"nothing recognisable", []string{`"0N70ZyFoz8n50" yEnc (1/5)`}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			arts := make([]stagedArticle, len(c.subs))
			for i, s := range c.subs {
				arts[i] = subjArt(s)
			}
			if got := contentKindFromArticles(arts); got != c.want {
				t.Errorf("contentKindFromArticles = %q, want %q", got, c.want)
			}
		})
	}
}

// A real set is one media file plus par2s plus an nfo and often a sample jpg,
// so the answer must come from a stable priority rather than from whichever
// article happens to come first — map iteration order alone would classify a
// large share of the catalogue as an image.
func TestContentKindIsNotDecidedByArticleOrder(t *testing.T) {
	book := []stagedArticle{
		subjArt(`"cover.jpg" yEnc (1/1)`),
		subjArt(`"readme.nfo" yEnc (1/1)`),
		subjArt(`"A Novel.epub" yEnc (1/3)`),
		subjArt(`"A Novel.vol000+01.par2" yEnc (1/2)`),
	}
	if got := contentKindFromArticles(book); got != kindBook {
		t.Errorf("book set with a cover and par2s = %q, want %q", got, kindBook)
	}
	// Reversed: same verdict. Priority, not position.
	for i, j := 0, len(book)-1; i < j; i, j = i+1, j-1 {
		book[i], book[j] = book[j], book[i]
	}
	if got := contentKindFromArticles(book); got != kindBook {
		t.Errorf("same set reversed = %q, want %q — the answer depends on order", got, kindBook)
	}
}

// Archives and recovery volumes describe the PACKAGING. Every obfuscated post
// on the index is rar volumes too, so treating them as evidence would vouch for
// the entire population the junk rules exist to catch.
func TestArchivesAreNotEvidenceOfAnything(t *testing.T) {
	for _, subs := range [][]string{
		{`"BB520.part001.rar" - (001/225) - yEnc (100/391)`},
		{`"0N70ZyFoz8n50.r00" yEnc (1/50)`},
		{`"blob.7z.001" yEnc (1/9)`, `"blob.7z.002" yEnc (1/9)`},
		{`"x.vol000+001.par2" yEnc (1/3)`},
		{`"y.zip" yEnc (1/3)`},
	} {
		arts := make([]stagedArticle, len(subs))
		for i, s := range subs {
			arts[i] = subjArt(s)
		}
		if got := contentKindFromArticles(arts); got != "" {
			t.Errorf("%v classified as %q — a container is not evidence of content", subs, got)
		}
	}
}

// The extension has to end a filename token. Substring matching would find
// ".ts" inside "rights", ".ape" inside "landscape" and ".iso" inside
// "isolation" — and the titles this guards against are long enough to contain
// anything.
func TestExtensionMustEndAFilenameToken(t *testing.T) {
	for _, s := range []string{
		`"Human.Rights.tsv" yEnc (1/2)`,
		`"landscape.mkvtoolnix" yEnc (1/2)`,
		`"isolation.txt" yEnc (1/2)`,
		`"episode.mp3x" yEnc (1/2)`,
	} {
		if got := contentKindFromArticles([]stagedArticle{subjArt(s)}); got == kindAudio {
			t.Errorf("%q classified as audio on a substring match", s)
		}
	}
	// The real forms still resolve.
	if got := contentKindFromArticles([]stagedArticle{subjArt(`"ep01.ts" yEnc (1/9)`)}); got != kindVideo {
		t.Errorf(`"ep01.ts" = %q, want video`, got)
	}
	if got := contentKindFromArticles([]stagedArticle{subjArt(`"track.ape" yEnc (1/9)`)}); got != kindAudio {
		t.Errorf(`"track.ape" = %q, want audio`, got)
	}
}

// articlesContainComicArchive is the original comics-only form and callers
// still depend on it, so it must keep answering exactly as before.
func TestComicArchiveHelperStillAnswers(t *testing.T) {
	if !articlesContainComicArchive([]stagedArticle{subjArt(`"Vol 3.cbr" yEnc (1/9)`)}) {
		t.Error("a .cbr set is no longer recognised as a comic archive")
	}
	if articlesContainComicArchive([]stagedArticle{subjArt(`"Show.S01E01.mkv" yEnc (1/9)`)}) {
		t.Error("a video set was reported as a comic archive")
	}
}
