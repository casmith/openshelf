package ui

var iconPaths = map[string]string{
	"home":     `<path d="M3 10.5 12 3l9 7.5V20a1 1 0 0 1-1 1h-5v-6H9v6H4a1 1 0 0 1-1-1z"/>`,
	"library":  `<rect x="3" y="4" width="4" height="16" rx="1"/><rect x="9" y="4" width="4" height="16" rx="1"/><path d="m15.5 5 3.9-1 2.6 15.5-3.9 1z"/>`,
	"journal":  `<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>`,
	"list":     `<path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"/>`,
	"stats":    `<path d="M3 21h18M6 17v-6M11 17V5M16 17v-9M21 17v-3"/>`,
	"search":   `<circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/>`,
	"game":     `<rect x="2" y="7" width="20" height="11" rx="4"/><path d="M7 11v3M5.5 12.5h3M15 12h.01M18 13.5h.01"/>`,
	"book":     `<path d="M4 19.5V5a2 2 0 0 1 2-2h14v16H6.5a2.5 2.5 0 0 0 0 5H20"/>`,
	"film":     `<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M7 3v18M17 3v18M3 8h4M3 16h4M17 8h4M17 16h4M3 12h18"/>`,
	"show":     `<rect x="2" y="7" width="20" height="14" rx="2"/><path d="m17 2-5 5-5-5"/>`,
	"plus":     `<path d="M12 5v14M5 12h14"/>`,
	"user":     `<circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0 1 16 0"/>`,
	"logout":   `<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9"/>`,
	"settings": `<path d="M4 6h10M18 6h2M4 12h4M12 12h8M4 18h12M20 18h0"/><circle cx="16" cy="6" r="2"/><circle cx="10" cy="12" r="2"/><circle cx="18" cy="18" r="2"/>`,
	"check":    `<path d="M20 6 9 17l-5-5"/>`,
	"x":        `<path d="M18 6 6 18M6 6l12 12"/>`,
	"menu":     `<path d="M4 6h16M4 12h16M4 18h16"/>`,
	"up":       `<path d="m18 15-6-6-6 6"/>`,
	"down":     `<path d="m6 9 6 6 6-6"/>`,
	"left":     `<path d="m15 18-6-6 6-6"/>`,
	"right":    `<path d="m9 18 6-6-6-6"/>`,
	"star":     `<path d="m12 2 3.1 6.3 6.9 1-5 4.9 1.2 6.8L12 17.8 5.8 21l1.2-6.8-5-4.9 6.9-1z"/>`,
	"trash":    `<path d="M3 6h18M8 6V4h8v2M19 6l-1 14H6L5 6"/>`,
	"edit":     `<path d="M12 20h9M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z"/>`,
	"download": `<path d="M12 3v12M7 10l5 5 5-5M4 21h16"/>`,
}

func iconSVG(name string) string {
	return iconPaths[name]
}
