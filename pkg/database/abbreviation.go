package database

// AbbreviateDescription truncates description to at most 200 runes (first 197 + "...").
func AbbreviateDescription(desc string) string {
	r := []rune(desc)
	if len(r) <= 200 {
		return desc
	}
	return string(r[:197]) + "..."
}

// AbbreviateScript truncates script to at most 1000 runes ("..." + last 997).
func AbbreviateScript(script string) string {
	r := []rune(script)
	if len(r) <= 1000 {
		return script
	}
	return "..." + string(r[len(r)-997:])
}
