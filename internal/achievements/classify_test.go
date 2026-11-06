package achievements

import (
	"strings"
	"testing"
	"time"
)

func ach(s string) Achievement {
	name, desc, _ := strings.Cut(s, " :: ")
	return Achievement{Name: name, Description: desc}
}

func TestPhrase(t *testing.T) {
	cases := []struct {
		title, text string
		want        Level
	}{
		{"Tyranny", "Felingard Loremaster :: Completed the main quest", Strong},
		{"Portal", "Heartbreaker :: Complete Portal", Strong},
		{"Gris", "The End :: See the epilogue", Strong},
		{"Celeste", "The End :: Take a nice long nap", Strong},
		{"Hades", "Credits :: Watch the end credits", Strong},
		{"Halo", "Legendary :: Finish the campaign on Legendary", Strong},
		{"Doom", "Ultimate Evil :: Defeat the final boss", Strong},
		{"Braid", "Happy Ending :: Reach the good ending", Strong},
		{"Myst", "Game Complete", Strong},
		{"Journey", "Long, Strange Trip :: Complete all Areas of Journey Mode", Medium},
		{"Dawn of War", "Intergalactic Headbutt :: Finish the Ork story.", Medium},
		{"Thimbleweed Park", "Out of the Frying Pan :: Complete chapter 1 of episode 1.", None},
		{"Talos", "Thinker :: Finish Chapter I.", None},
		{"Journey", "With a Single Step :: Complete Area 1 of Journey Mode.", None},
		{"Doom", "E1M1 :: Complete the first mission of the campaign", None},
		{"Portal 2", "Didn't Skip the Tutorial :: Complete tutorial", None},
		{"Ratchet", "Kinetic Harvest :: Complete World 1 in Story Mode.", None},
		{"Hollow", "Prologue Complete", None},
		{"Quake", "Sink the Horror Electric :: Complete Quake Episode 1: Dimension of the Doomed in single player.", None},
		{"Elite", "Rich :: Earn 1,000,000 credits", None},
		{"Spelunky", "The End of the Line :: Reach the end of the mines", None},
		{"Neverending", "Never-ending :: Play for 100 hours", None},
		{"Talos", "Messenger :: Finish the final chapter", Strong},
		{"Talos", "Chapter 7 :: Finish chapter 7, the last chapter", Strong},
		{"Portal", "Portal Gun :: Get the portal gun", None},
	}
	for _, c := range cases {
		if got := Phrase(c.title, ach(c.text)); got != c.want {
			t.Errorf("Phrase(%q, %q) = %v, want %v", c.title, c.text, got, c.want)
		}
	}
}

func TestClassifyEvidence(t *testing.T) {
	at := func(d int) *time.Time {
		v := time.Date(2021, 11, d, 12, 0, 0, 0, time.UTC)
		return &v
	}
	strongA := Achievement{Name: "The End", Description: "See the epilogue", At: at(10)}
	strongB := Achievement{Name: "Credits", Description: "Watch the end credits", At: at(20)}
	medium := Achievement{Name: "All Areas", Description: "Complete all areas", At: at(5)}
	latest := Achievement{Name: "Last one", At: at(26)}

	v := Classify(Game{Title: "X", Unlocked: 17, Total: 17, Latest: latest, Candidates: []Achievement{medium, strongA, strongB}})
	if v.Level != Strong || v.Evidence != strongA || v.AllUnlocked {
		t.Errorf("strong: got %+v", v)
	}
	v = Classify(Game{Title: "X", Unlocked: 17, Total: 17, Latest: latest})
	if v.Level != Medium || v.Evidence != latest || !v.AllUnlocked {
		t.Errorf("17/17: got %+v", v)
	}
	v = Classify(Game{Title: "X", Unlocked: 3, Total: 17, Latest: latest, Candidates: []Achievement{medium}})
	if v.Level != Medium || v.Evidence != medium {
		t.Errorf("phrase medium: got %+v", v)
	}
	if v := Classify(Game{Title: "X", Unlocked: 4, Total: 4, Latest: latest}); v.Level != None {
		t.Errorf("4/4 should not count: got %+v", v)
	}
	if v := Classify(Game{Title: "X", Unlocked: 16, Total: 17, Latest: latest, Candidates: []Achievement{ach("Thinker :: Finish Chapter I.")}}); v.Level != None {
		t.Errorf("partial: got %+v", v)
	}
}
