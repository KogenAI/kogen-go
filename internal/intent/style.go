package intent

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"kogen-go/internal/cli/data"
)

// The language-neutral corpus is frozen and shared with the CLI's normative
// data. Keeping tier limits, word lists and messages in that corpus prevents
// this pass from drifting from the format contract.
type lintCorpus struct {
	Tiers         map[string]lintTier `json:"tiers"`
	Limits        lintLimits          `json:"limits"`
	BannedWords   []string            `json:"banned_words"`
	BannedPhrases []string            `json:"banned_phrases"`
	HedgeWords    []string            `json:"hedge_words"`
	HedgePhrases  []string            `json:"hedge_phrases"`
	ActionVerbs   []string            `json:"action_verbs"`
}

type lintTier struct {
	BriefParagraphs int `json:"brief_paragraphs"`
	BriefWords      int `json:"brief_words"`
	Items           int `json:"items"`
	NotesWords      int `json:"notes_words"`
}

type lintLimits struct {
	TitleChars          int `json:"title_chars"`
	ItemWords           int `json:"item_words"`
	SentenceWords       int `json:"sentence_words"`
	NotesCodeBlockLines int `json:"notes_code_block_lines"`
	ApproachMinWords    int `json:"approach_min_words"`
}

var normativeLint = loadLintCorpus()

func loadLintCorpus() lintCorpus {
	contents, err := data.ReadFile("lint.json")
	if err != nil {
		panic(fmt.Sprintf("read embedded lint corpus: %v", err))
	}
	var corpus lintCorpus
	if err := json.Unmarshal(contents, &corpus); err != nil {
		panic(fmt.Sprintf("decode embedded lint corpus: %v", err))
	}
	return corpus
}

func styleFindings(intent *Intent, shaping bool) []LintIssue {
	var issues []LintIssue
	lintTiers(intent, &issues)
	lintTitle(intent, &issues)
	lintItems(intent, &issues)
	lintWordsAndPhrases(intent, &issues)
	lintSentences(intent, &issues)
	lintNotes(intent, shaping, &issues)
	return issues
}

// NormalizeNotes applies the shape formatter's Approach prefix rule. It
// returns the rewritten Notes and true when the bytes should be replaced.
func NormalizeNotes(notes string) (string, bool) {
	trimmed := strings.TrimLeftFunc(notes, unicode.IsSpace)
	if hasApproachPrefix(trimmed) {
		suffix := trimmed[len("Approach:"):]
		if suffix == "" || !isSpaceRune(firstRune(suffix)) {
			suffix = " " + suffix
		}
		normalized := "Approach:" + suffix
		return normalized, normalized != notes
	}
	if validApproachBody(trimmed) {
		normalized := "Approach: " + trimmed
		return normalized, normalized != notes
	}
	return "", false
}

func lintTiers(intent *Intent, issues *[]LintIssue) {
	tier, ok := normativeLint.Tiers[intent.Frontmatter.Size]
	if !ok {
		return
	}
	if paragraphs := paragraphCount(intent.Brief); paragraphs > tier.BriefParagraphs {
		*issues = append(*issues, styleIssue("brief_paragraphs", nil, fmt.Sprintf("%s Intents allow at most %d Brief paragraphs", intent.Frontmatter.Size, tier.BriefParagraphs)))
	}
	if count := wordCount(intent.Brief); count > tier.BriefWords {
		*issues = append(*issues, styleIssue("brief_too_long", nil, fmt.Sprintf("%s Intents allow at most %d Brief words", intent.Frontmatter.Size, tier.BriefWords)))
	}
	if len(intent.Acceptance) > tier.Items {
		*issues = append(*issues, styleIssue("too_many_items", nil, fmt.Sprintf("%s Intents allow at most %d Acceptance items", intent.Frontmatter.Size, tier.Items)))
	}
	if count := wordCount(intent.Notes); count > tier.NotesWords {
		*issues = append(*issues, styleIssue("notes_too_long", nil, fmt.Sprintf("%s Intents allow at most %d Notes words", intent.Frontmatter.Size, tier.NotesWords)))
	}
}

func lintTitle(intent *Intent, issues *[]LintIssue) {
	limit := normativeLint.Limits.TitleChars
	if runeCount(intent.Frontmatter.Title) > limit {
		*issues = append(*issues, styleIssue("title_too_long", nil, fmt.Sprintf("title must be at most %d characters", limit)))
	}
}

func lintItems(intent *Intent, issues *[]LintIssue) {
	itemLimit := normativeLint.Limits.ItemWords
	sentenceLimit := normativeLint.Limits.SentenceWords
	for _, item := range intent.Acceptance {
		if wordCount(item.Text) > itemLimit {
			*issues = append(*issues, styleIssue("item_too_long", intPointer(item.Line), fmt.Sprintf("%s exceeds %d words", item.ID, itemLimit)))
		}
		plain := withoutInlineCode(item.Text)
		if hasAnyTerm(plain, normativeLint.HedgeWords) || hasAnyTerm(plain, normativeLint.HedgePhrases) {
			*issues = append(*issues, styleIssue("hedge", intPointer(item.Line), fmt.Sprintf("%s contains a hedge; state an observable result", item.ID)))
		}
		lintBanned(item.Text, item.ID, intPointer(item.Line), issues)
		if anyLongSentence(item.Text, sentenceLimit) {
			*issues = append(*issues, styleIssue("sentence_too_long", intPointer(item.Line), fmt.Sprintf("%s has a sentence over %d words", item.ID, sentenceLimit)))
		}
	}
}

func lintWordsAndPhrases(intent *Intent, issues *[]LintIssue) {
	lintBanned(intent.Brief, "Brief", nil, issues)
	plain := withoutInlineCode(intent.Brief)
	if hasAnyTerm(plain, normativeLint.HedgeWords) || hasAnyTerm(plain, normativeLint.HedgePhrases) {
		*issues = append(*issues, styleIssue("hedge", nil, "Brief contains a hedge; state an observable result"))
	}
}

func lintBanned(text, section string, line *int, issues *[]LintIssue) {
	plain := withoutInlineCode(text)
	for _, phrase := range normativeLint.BannedWords {
		if containsTerm(plain, phrase) {
			*issues = append(*issues, styleIssue("banned_phrase", line, fmt.Sprintf("%s contains banned phrase %q", section, phrase)))
		}
	}
	for _, phrase := range normativeLint.BannedPhrases {
		if containsTerm(plain, phrase) {
			*issues = append(*issues, styleIssue("banned_phrase", line, fmt.Sprintf("%s contains banned phrase %q", section, phrase)))
		}
	}
}

func lintSentences(intent *Intent, issues *[]LintIssue) {
	limit := normativeLint.Limits.SentenceWords
	if anyLongSentence(intent.Brief, limit) {
		*issues = append(*issues, styleIssue("sentence_too_long", nil, fmt.Sprintf("Brief has a sentence over %d words", limit)))
	}
}

func lintNotes(intent *Intent, shaping bool, issues *[]LintIssue) {
	codeLimit := normativeLint.Limits.NotesCodeBlockLines
	var fence string
	codeLines := 0
	for _, line := range strings.Split(intent.Notes, "\n") {
		trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
				if codeLines > codeLimit {
					*issues = append(*issues, styleIssue("long_code_block", nil, fmt.Sprintf("Notes code blocks must contain at most %d lines", codeLimit)))
				}
			} else {
				codeLines++
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			fence = "```"
			codeLines = 0
		} else if strings.HasPrefix(trimmed, "~~~") {
			fence = "~~~"
			codeLines = 0
		}
	}
	if fence != "" && codeLines > codeLimit {
		*issues = append(*issues, styleIssue("long_code_block", nil, fmt.Sprintf("Notes code blocks must contain at most %d lines", codeLimit)))
	}
	if shaping && !validApproach(intent.Notes) {
		min := normativeLint.Limits.ApproachMinWords
		*issues = append(*issues, styleIssue("missing_approach", nil, fmt.Sprintf("Notes must start with Approach: naming the code path (at least %d words and an action verb)", min)))
	}
}

func validApproach(notes string) bool {
	trimmed := strings.TrimLeftFunc(notes, unicode.IsSpace)
	if hasApproachPrefix(trimmed) {
		trimmed = strings.TrimLeftFunc(trimmed[len("Approach:"):], unicode.IsSpace)
	}
	return validApproachBody(trimmed)
}

func validApproachBody(text string) bool {
	tokens := strings.Fields(text)
	return len(tokens) >= normativeLint.Limits.ApproachMinWords && len(tokens) > 0 && hasAnyTerm(tokens[0], normativeLint.ActionVerbs)
}

func hasApproachPrefix(text string) bool {
	return len(text) >= len("Approach:") && asciiEqualFold(text[:len("Approach:")], "Approach:")
}

func firstRune(text string) rune {
	for _, character := range text {
		return character
	}
	return 0
}

func isSpaceRune(character rune) bool { return unicode.IsSpace(character) }

func hasAnyTerm(text string, terms []string) bool {
	for _, term := range terms {
		if containsTerm(text, term) {
			return true
		}
	}
	return false
}

func containsTerm(text, term string) bool {
	lowerText := asciiLower(text)
	lowerTerm := asciiLower(term)
	for offset := 0; offset <= len(lowerText)-len(lowerTerm); {
		index := strings.Index(lowerText[offset:], lowerTerm)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(lowerTerm)
		if asciiWordBoundaryBefore(lowerText, start) && asciiWordBoundaryAfter(lowerText, end) {
			return true
		}
		offset = start + 1
	}
	return false
}

func withoutInlineCode(text string) string {
	var output strings.Builder
	output.Grow(len(text))
	inside := false
	for _, character := range text {
		if character == '`' {
			inside = !inside
			output.WriteByte(' ')
		} else if inside {
			output.WriteByte(' ')
		} else {
			output.WriteRune(character)
		}
	}
	return output.String()
}

func paragraphCount(text string) int {
	count := 0
	inParagraph := false
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			if inParagraph {
				count++
				inParagraph = false
			}
		} else {
			inParagraph = true
		}
	}
	if inParagraph {
		count++
	}
	return count
}

func anyLongSentence(text string, limit int) bool {
	for _, sentence := range splitSentences(text) {
		if wordCount(sentence) > limit {
			return true
		}
	}
	return false
}

func splitSentences(text string) []string {
	runes := []rune(text)
	var sentences []string
	start := 0
	for index, character := range runes {
		if (character == '.' || character == '!' || character == '?') && index+1 < len(runes) && unicode.IsSpace(runes[index+1]) {
			sentence := strings.TrimSpace(string(runes[start : index+1]))
			if sentence != "" {
				sentences = append(sentences, sentence)
			}
			start = index + 1
		}
	}
	if sentence := strings.TrimSpace(string(runes[start:])); sentence != "" {
		sentences = append(sentences, sentence)
	}
	return sentences
}

func wordCount(text string) int { return len(strings.Fields(text)) }

func runeCount(text string) int { return len([]rune(text)) }

func asciiLower(text string) string {
	bytes := []byte(text)
	for index, value := range bytes {
		if value >= 'A' && value <= 'Z' {
			bytes[index] = value + ('a' - 'A')
		}
	}
	return string(bytes)
}

func asciiUpper(text string) string {
	bytes := []byte(text)
	for index, value := range bytes {
		if value >= 'a' && value <= 'z' {
			bytes[index] = value - ('a' - 'A')
		}
	}
	return string(bytes)
}

func asciiEqualFold(left, right string) bool { return asciiLower(left) == asciiLower(right) }

func styleIssue(rule string, line *int, message string) LintIssue {
	return LintIssue{Rule: rule, Severity: LintStyle, Line: line, Message: message}
}
