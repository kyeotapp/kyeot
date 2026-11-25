package hermesbridge

// app language → instruction attached to every run; the Hermes config file is not modified
var langInstructions = map[string]string{
	"ko": "The user's app language is Korean. Reply and title this conversation in Korean unless asked otherwise.",
	"en": "The user's app language is English. Reply and title this conversation in English unless asked otherwise.",
}

// runInstructions is the run instruction for the app language; empty for an unknown language.
func runInstructions(lang string) string { return langInstructions[lang] }
