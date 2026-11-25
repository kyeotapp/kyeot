package linkpreview

import (
	"html"
	"regexp"
	"strings"
)

// Scans only the tags the preview needs (meta, link, title). A full HTML parser would be a new dependency, so none is used.
var (
	tagRe   = regexp.MustCompile(`(?is)<(meta|link)\b([^>]*)>`)
	attrRe  = regexp.MustCompile(`(?s)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'>]+))`)
	titleRe = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title>`)
)

type htmlMeta struct {
	title, description, site, image, icon string
}

func parseHTML(doc string) htmlMeta {
	var og, plain htmlMeta
	for _, m := range tagRe.FindAllStringSubmatch(doc, -1) {
		attrs := map[string]string{}
		for _, a := range attrRe.FindAllStringSubmatch(m[2], -1) {
			attrs[strings.ToLower(a[1])] = html.UnescapeString(a[2] + a[3] + a[4])
		}
		if strings.EqualFold(m[1], "link") {
			if plain.icon == "" && hasToken(attrs["rel"], "icon") {
				plain.icon = strings.TrimSpace(attrs["href"])
			}
			continue
		}
		key := strings.ToLower(attrs["property"])
		if key == "" {
			key = strings.ToLower(attrs["name"])
		}
		val := attrs["content"]
		switch key {
		case "og:title":
			setOnce(&og.title, val)
		case "og:description":
			setOnce(&og.description, val)
		case "og:site_name":
			setOnce(&og.site, val)
		case "og:image", "og:image:url", "og:image:secure_url":
			setOnce(&og.image, strings.TrimSpace(val))
		case "description":
			setOnce(&plain.description, val)
		}
	}
	if m := titleRe.FindStringSubmatch(doc); m != nil {
		plain.title = html.UnescapeString(m[1])
	}
	return htmlMeta{
		title:       clip(first(og.title, plain.title), maxTitle),
		description: clip(first(og.description, plain.description), maxDesc),
		site:        clip(og.site, maxSite),
		image:       og.image,
		icon:        plain.icon,
	}
}

func setOnce(dst *string, v string) {
	if *dst == "" {
		*dst = strings.TrimSpace(v)
	}
}

func first(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func hasToken(list, tok string) bool {
	for _, f := range strings.Fields(strings.ToLower(list)) {
		if f == tok {
			return true
		}
	}
	return false
}
