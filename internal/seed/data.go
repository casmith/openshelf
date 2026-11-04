package seed

type credit struct{ name, role string }

type edition struct {
	format   string
	platform string
	pages    int
}

type item struct {
	kind     string
	title    string
	year     int
	genres   []string
	synopsis string
	credits  []credit
	editions []edition
}

func by(role string, names ...string) []credit {
	out := make([]credit, len(names))
	for i, n := range names {
		out[i] = credit{n, role}
	}
	return out
}

func on(platforms ...string) []edition {
	out := make([]edition, len(platforms))
	for i, p := range platforms {
		out[i] = edition{format: "digital", platform: p}
	}
	return out
}

func paperback(pages int) []edition { return []edition{{format: "paperback", pages: pages}} }

var items = []item{
	{"game", "Valheim", 2021, []string{"survival", "sandbox"}, "A brutal exploration and survival game set in a procedurally generated purgatory inspired by Viking culture.", by("developer", "Iron Gate"), on("pc")},
	{"game", "Elden Ring", 2022, []string{"action rpg", "open world"}, "Rise, Tarnished, and be guided by grace to brandish the power of the Elden Ring.", by("developer", "FromSoftware"), on("pc", "ps5")},
	{"game", "Baldur's Gate 3", 2023, []string{"rpg"}, "Gather your party and return to the Forgotten Realms.", by("developer", "Larian Studios"), on("pc", "ps5")},
	{"game", "Hades", 2020, []string{"roguelike", "action"}, "Defy the god of the dead as you hack and slash out of the Underworld.", by("developer", "Supergiant Games"), on("pc", "switch")},
	{"game", "Shenmue", 1999, []string{"adventure"}, "Ryo Hazuki returns home to find his father murdered and sets out for revenge.", by("developer", "Sega AM2"), []edition{{format: "physical", platform: "dreamcast"}}},
	{"game", "Jet Set Radio", 2000, []string{"action", "platformer"}, "Skate, tag, and outrun the police across Tokyo-to.", by("developer", "Smilebit"), []edition{{format: "physical", platform: "dreamcast"}}},
	{"game", "Rez", 2001, []string{"rail shooter", "music"}, "A synesthetic rail shooter where every shot adds to the soundtrack.", by("developer", "United Game Artists"), []edition{{format: "physical", platform: "dreamcast"}, {format: "physical", platform: "ps2"}}},
	{"game", "Skies of Arcadia", 2000, []string{"jrpg"}, "Sky pirates Vyse and Aika take on the Valuan Empire.", by("developer", "Overworks"), []edition{{format: "physical", platform: "dreamcast"}, {format: "physical", platform: "gamecube"}}},
	{"game", "Ico", 2001, []string{"adventure", "puzzle"}, "A horned boy and a luminous girl escape a vast, empty castle.", by("developer", "Team Ico"), []edition{{format: "physical", platform: "ps2"}}},
	{"game", "Shadow of the Colossus", 2005, []string{"action", "adventure"}, "Defeat sixteen colossi to bring a girl back to life.", by("developer", "Team Ico"), []edition{{format: "physical", platform: "ps2"}}},
	{"game", "Super Metroid", 1994, []string{"metroidvania"}, "Samus Aran returns to planet Zebes to recover a stolen Metroid larva.", by("developer", "Nintendo R&D1"), []edition{{format: "physical", platform: "snes"}}},
	{"game", "Portal 2", 2011, []string{"puzzle"}, "Return to Aperture Science with a portal gun and an AI with a grudge.", by("developer", "Valve"), on("pc", "xbox-360", "ps3")},
	{"game", "Okami", 2006, []string{"action", "adventure"}, "The sun goddess Amaterasu restores color to a cursed land.", by("developer", "Clover Studio"), []edition{{format: "physical", platform: "ps2"}, {format: "physical", platform: "wii"}}},
	{"game", "Hollow Knight", 2017, []string{"metroidvania"}, "Descend into the ruined insect kingdom of Hallownest.", by("developer", "Team Cherry"), on("pc", "switch")},
	{"game", "Outer Wilds", 2019, []string{"exploration", "puzzle"}, "A solar system trapped in an endless time loop.", by("developer", "Mobius Digital"), on("pc")},
	{"game", "Disco Elysium", 2019, []string{"rpg"}, "A detective with amnesia solves a murder in a city on the edge.", by("developer", "ZA/UM"), on("pc")},

	{"book", "Pandora's Star", 2004, []string{"science fiction", "space opera"}, "In the 24th century humanity has colonized hundreds of worlds, until an astronomer sees a star vanish.", by("author", "Peter F. Hamilton"), paperback(1496)},
	{"book", "Dune", 1965, []string{"science fiction"}, "Paul Atreides and the desert planet Arrakis.", by("author", "Frank Herbert"), paperback(688)},
	{"book", "Project Hail Mary", 2021, []string{"science fiction"}, "A lone astronaut wakes with no memory and a mission to save Earth.", by("author", "Andy Weir"), paperback(496)},
	{"book", "Leviathan Wakes", 2011, []string{"science fiction", "space opera"}, "A missing girl, a derelict ship, and a conspiracy across the solar system.", by("author", "James S. A. Corey"), paperback(592)},
	{"book", "A Memory Called Empire", 2019, []string{"science fiction"}, "An ambassador arrives in the heart of a multi-system empire to find her predecessor dead.", by("author", "Arkady Martine"), paperback(464)},
	{"book", "Anathem", 2008, []string{"science fiction"}, "Cloistered scholars confront a world-shaking discovery.", by("author", "Neal Stephenson"), paperback(960)},
	{"book", "The Left Hand of Darkness", 1969, []string{"science fiction"}, "An envoy to the icebound planet Gethen.", by("author", "Ursula K. Le Guin"), paperback(304)},
	{"book", "Piranesi", 2020, []string{"fantasy"}, "A man lives in an infinite house of statues and tides.", by("author", "Susanna Clarke"), paperback(272)},
	{"book", "Children of Time", 2015, []string{"science fiction"}, "The last humans find a terraformed world that is already occupied.", by("author", "Adrian Tchaikovsky"), paperback(608)},
	{"book", "The Fifth Season", 2015, []string{"fantasy"}, "A world that ends every few centuries.", by("author", "N. K. Jemisin"), paperback(512)},
	{"book", "Hyperion", 1989, []string{"science fiction"}, "Seven pilgrims journey to the Time Tombs.", by("author", "Dan Simmons"), paperback(482)},

	{"film", "Blade Runner 2049", 2017, []string{"science fiction"}, "A young blade runner uncovers a secret that could plunge society into chaos.", by("director", "Denis Villeneuve"), []edition{{format: "uhd_bluray"}}},
	{"film", "Interstellar", 2014, []string{"science fiction"}, "Explorers travel through a wormhole in search of a new home for humanity.", by("director", "Christopher Nolan"), []edition{{format: "bluray"}}},
	{"film", "Arrival", 2016, []string{"science fiction", "drama"}, "A linguist is recruited to communicate with alien visitors.", by("director", "Denis Villeneuve"), nil},
	{"film", "Dune: Part Two", 2024, []string{"science fiction"}, "Paul Atreides unites with the Fremen.", by("director", "Denis Villeneuve"), nil},
	{"film", "Spirited Away", 2001, []string{"animation", "fantasy"}, "A girl wanders into a world of spirits.", by("director", "Hayao Miyazaki"), []edition{{format: "bluray"}}},
	{"film", "Alien", 1979, []string{"science fiction", "horror"}, "The crew of the Nostromo answers a distress call.", by("director", "Ridley Scott"), nil},
	{"film", "Mad Max: Fury Road", 2015, []string{"action"}, "A desert chase across a post-apocalyptic wasteland.", by("director", "George Miller"), nil},
	{"film", "The Thing", 1982, []string{"horror", "science fiction"}, "An Antarctic research team meets a shape-shifting alien.", by("director", "John Carpenter"), nil},

	{"show", "The Expanse", 2015, []string{"science fiction"}, "A detective, a ship's officer, and a UN executive unravel a conspiracy.", by("creator", "Mark Fergus", "Hawk Ostby"), nil},
	{"show", "Arcane", 2021, []string{"animation", "fantasy"}, "Two sisters on opposite sides of a war between Piltover and Zaun.", by("creator", "Christian Linke", "Alex Yee"), nil},
	{"show", "Severance", 2022, []string{"thriller", "science fiction"}, "Office workers have their memories surgically divided between work and home.", by("creator", "Dan Erickson"), nil},
	{"show", "Andor", 2022, []string{"science fiction"}, "Cassian Andor's path to rebellion.", by("creator", "Tony Gilroy"), nil},
	{"show", "Twin Peaks", 1990, []string{"mystery", "drama"}, "An FBI agent investigates the murder of a homecoming queen.", by("creator", "David Lynch", "Mark Frost"), nil},
	{"show", "Battlestar Galactica", 2004, []string{"science fiction"}, "The survivors of humanity flee the Cylons.", by("creator", "Ronald D. Moore"), nil},
	{"show", "The Sandman", 2022, []string{"fantasy"}, "Dream of the Endless rebuilds his realm.", by("creator", "Neil Gaiman"), nil},
}

type state struct {
	ownership  string // "" = none
	backlog    bool
	experience string // "" none; active, paused, finished, abandoned, rewatched
	unit       string
	current    float64
	total      float64
	rating     int // 0 = no review
	review     string
	visibility string
	spoilers   bool
}

var aliceLibrary = map[string]state{
	"Valheim":           {ownership: "owned", experience: "active", unit: "hours", current: 64},
	"Elden Ring":        {ownership: "owned", experience: "finished", rating: 95, review: "Every corner of the Lands Between rewards curiosity.", visibility: "public"},
	"Baldur's Gate 3":   {ownership: "owned", backlog: true},
	"Hades":             {ownership: "owned", experience: "finished", rating: 90, visibility: "instance"},
	"Shenmue":           {ownership: "owned", experience: "finished", rating: 80},
	"Jet Set Radio":     {ownership: "owned", backlog: true},
	"Rez":               {ownership: "owned", experience: "finished", rating: 85},
	"Skies of Arcadia":  {ownership: "owned", backlog: true},
	"Portal 2":          {ownership: "owned"},
	"Outer Wilds":       {ownership: "owned", backlog: true},
	"Disco Elysium":     {ownership: "owned", experience: "paused", unit: "percent", current: 40, total: 100},
	"Hollow Knight":     {ownership: "owned", experience: "abandoned"},
	"Pandora's Star":    {ownership: "owned", experience: "active", unit: "pages", current: 1079, total: 1496},
	"Dune":              {ownership: "owned", experience: "finished", rating: 100, review: "Still the benchmark.", visibility: "private"},
	"Project Hail Mary": {ownership: "owned", experience: "finished", rating: 85, review: "Rocky is the best character of the decade.", visibility: "instance"},
	"Anathem":           {ownership: "owned", experience: "abandoned"},
	"Piranesi":          {ownership: "borrowed", experience: "finished", rating: 90},
	"Children of Time":  {backlog: true},
	"Hyperion":          {ownership: "owned", backlog: true},
	"Blade Runner 2049": {ownership: "owned", experience: "finished", rating: 90},
	"Interstellar":      {ownership: "owned", experience: "rewatched", rating: 90, review: "The docking scene. Cooper's messages from home wreck me every time.", visibility: "public", spoilers: true},
	"Arrival":           {experience: "finished", rating: 85},
	"Dune: Part Two":    {backlog: true},
	"Spirited Away":     {ownership: "owned", experience: "finished", rating: 100},
	"The Expanse":       {experience: "active", unit: "episodes", current: 30, total: 62},
	"Arcane":            {experience: "finished", rating: 95, visibility: "public"},
	"Severance":         {experience: "active", unit: "episodes", current: 12, total: 19},
	"Andor":             {backlog: true},
	"Twin Peaks":        {backlog: true},
}

var bobLibrary = map[string]state{
	"Dune":          {ownership: "owned", experience: "finished", rating: 70, review: "Bob's private thoughts.", visibility: "private"},
	"Hades":         {ownership: "owned", experience: "active", unit: "hours", current: 12},
	"Alien":         {experience: "finished", rating: 90, visibility: "instance"},
	"Super Metroid": {ownership: "owned", experience: "finished"},
	"The Thing":     {backlog: true},
}
