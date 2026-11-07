package steam

import "testing"

func TestIconURL(t *testing.T) {
	cases := map[string]string{
		"https://steamcdn-a.akamaihd.net/steamcommunity/public/images/apps/892970/c679.jpg": "https://shared.fastly.steamstatic.com/community_assets/images/apps/892970/c679.jpg",
		"https://shared.fastly.steamstatic.com/community_assets/images/apps/400/a.jpg":      "https://shared.fastly.steamstatic.com/community_assets/images/apps/400/a.jpg",
		"": "",
	}
	for in, want := range cases {
		if got := iconURL(in); got != want {
			t.Errorf("iconURL(%q) = %q, want %q", in, got, want)
		}
	}
}
