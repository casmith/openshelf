package achievements

import (
	"regexp"
	"strings"
	"time"
)

type Level int

const (
	None Level = iota
	Medium
	Strong
)

// minAllUnlocked is the smallest achievement set where 100% counts as evidence.
const minAllUnlocked = 5

type Achievement struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	At          *time.Time `json:"at"`
}

// Game is what the classifier sees for one unfinished game.
type Game struct {
	Title      string
	Unlocked   int
	Total      int
	Latest     Achievement   // most recent unlock
	Candidates []Achievement // unlocks in unlock order (nil times last)
}

var (
	strongRes = compile(
		`\bepilogue\b`,
		`\b(the|end|closing|final) credits\b|\bcredits roll`, // not in-game currency
		`(^|[^\w-])endings?\b`,
		`\bfinal boss\b`,
		`\bbeat the game\b`,
		`\bfinish(ed)? the game\b`,
		`\bgame complete(d)?\b`,
		`\bstory complete(d)?\b`,
		`\b(complete|completed|finish|finished) the (main )?(game|story|campaign|main quest|main story|adventure)\b`,
		`\bthe end of the (game|story|campaign|adventure|journey)\b`,
		`\b(final|last) (chapter|episode|act|world|area|level|mission|stage)\b`,
	)
	theEnd    = regexp.MustCompile(`\bthe end\b( of)?`)
	mediumRes = compile(
		`\b(complete|completed|finish|finished) (all|every) (the )?(areas|levels|chapters|missions|stages|worlds|episodes)\b`,
		`\b(complete|completed|finish|finished) (the |an? )?[\w'-]+ (story|campaign|storyline)\b`,
	)
	partialRes = compile(
		`\b(chapter|episode|act|world|area|level|mission|stage|part)\s+(\d+|[ivx]+|one|two|three|four|five|six|seven|eight|nine|ten)\b`,
		`\bthe first\b`,
		`\btutorial\b`,
		`\bprologue\b`,
	)
	finalRe = regexp.MustCompile(`\b(final|last)\b`)
)

func compile(exprs ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(exprs))
	for i, e := range exprs {
		out[i] = regexp.MustCompile(e)
	}
	return out
}

func anyMatch(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// Phrase classifies one achievement's text against the game's title.
func Phrase(title string, a Achievement) Level {
	t := strings.ToLower(a.Name + " :: " + a.Description)
	if anyMatch(partialRes, t) && !finalRe.MatchString(t) {
		return None
	}
	if anyMatch(strongRes, t) || endsGame(t) || completesTitle(t, title) {
		return Strong
	}
	if anyMatch(mediumRes, t) {
		return Medium
	}
	return None
}

// endsGame matches "the end" unless it's "the end of" something unrelated.
func endsGame(t string) bool {
	for _, m := range theEnd.FindAllStringSubmatch(t, -1) {
		if m[1] == "" {
			return true
		}
	}
	return false
}

func completesTitle(t, title string) bool {
	title = strings.TrimSpace(strings.ToLower(title))
	if len(title) < 3 {
		return false
	}
	re, err := regexp.Compile(`\bcomplete(d)? ` + regexp.QuoteMeta(title) + `($|[^\w])`)
	return err == nil && re.MatchString(t)
}

// Verdict is a game's classification and the achievement that supports it.
type Verdict struct {
	Level       Level
	Evidence    Achievement
	AllUnlocked bool       // evidence is the latest unlock of a fully unlocked game
	FinishAt    *time.Time // the ending's unlock, or the latest unlock when there's no ending
}

// Classify picks the earliest strong phrase, else the earliest medium phrase, else a full unlock.
func Classify(g Game) Verdict {
	var medium *Achievement
	for i, a := range g.Candidates {
		switch Phrase(g.Title, a) {
		case Strong:
			return Verdict{Level: Strong, Evidence: a, FinishAt: a.At}
		case Medium:
			if medium == nil {
				medium = &g.Candidates[i]
			}
		}
	}
	if medium != nil {
		return Verdict{Level: Medium, Evidence: *medium, FinishAt: g.Latest.At}
	}
	if g.Total >= minAllUnlocked && g.Unlocked >= g.Total {
		return Verdict{Level: Medium, Evidence: g.Latest, AllUnlocked: true, FinishAt: g.Latest.At}
	}
	return Verdict{}
}
