// Package translate provides text translation via remote Ollama OpenAI-compatible API.
// Supported content language codes: en, cn, es, fr, ar, de, jp, ru
package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// LangInfo describes a supported content language.
type LangInfo struct {
	Code    string // our internal code (matches i18n locale file names)
	Name    string // display name
	langName string // language name for AI prompts
	RTL     bool
}

// Langs is the ordered list of content languages supported for blog/product translations.
var Langs = []LangInfo{
	{"en", "English", "English", false},
	{"cn", "中文", "Simplified Chinese", false},
	{"es", "Español", "Spanish", false},
	{"fr", "Français", "French", false},
	{"ar", "العربية", "Arabic", true},
	{"de", "Deutsch", "German", false},
	{"ja", "日本語", "Japanese", false},
	{"ko", "한국어", "Korean", false},
	{"pt", "Português", "Portuguese", false},
	{"ru", "Русский", "Russian", false},
	{"th", "ภาษาไทย", "Thai", false},
}

var langNameMap = func() map[string]string {
	m := make(map[string]string, len(Langs))
	for _, l := range Langs {
		m[l.Code] = l.langName
	}
	return m
}()

var client = &http.Client{Timeout: 300 * time.Second}

// reThinkBlock strips <think>…</think> reasoning blocks emitted by Deepseek-R1
// and compatible models before the actual translation output.
var reThinkBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)

// Message is for OpenAI-compatible API
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the request format for OpenAI-compatible API
type ChatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// ChatResponse is the response format from OpenAI-compatible API
type ChatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int     `json:"index"`
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

var defaultOllamaEndpoint = "http://222.186.58.41:11434/v1/chat/completions"
var defaultModel = "deepseek-r1:8b"

func getOllamaEndpoint() string {
	// Check environment variable first
	if ep := os.Getenv("OLLAMA_URL"); ep != "" {
		return ep
	}
	// Default to remote Ollama deepseek-r1
	return defaultOllamaEndpoint
}

func getLangName(code string) string {
	if ln, ok := langNameMap[code]; ok {
		return ln
	}
	return code
}

// Text translates a short text via remote Ollama deepseek-r1:8b model.
// Uses default endpoint from settings or environment.
func Text(text, fromCode, toCode string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}
	return translateViaOpenAI(text, fromCode, toCode, "", "")
}

// TextWithEndpoint translates using a specific endpoint and model.
func TextWithEndpoint(text, fromCode, toCode, endpoint, model string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}
	return translateViaOpenAI(text, fromCode, toCode, endpoint, model)
}

// Long translates text of any length via Ollama (handles long text in one call).
// Uses default endpoint from settings or environment.
func Long(text, fromCode, toCode string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}
	return translateViaOpenAI(text, fromCode, toCode, "", "")
}

// LongWithEndpoint translates long text using a specific endpoint and model.
func LongWithEndpoint(text, fromCode, toCode, endpoint, model string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}
	return translateViaOpenAI(text, fromCode, toCode, endpoint, model)
}

// translateViaOpenAI sends a translation request to OpenAI-compatible Ollama endpoint
// If endpoint is empty, uses default. If model is empty, uses default.
//
// The result is validated against the target language's expected script (see
// looksLikeTargetLang). Weak models sometimes echo the source language instead
// of translating (observed: cn→ar and cn→th returning Chinese); when that
// happens the request is retried once with a stricter prompt, and if it still
// fails an error is returned rather than the wrong-language text — callers must
// not persist a failed translation.
func translateViaOpenAI(text, fromCode, toCode, endpoint, model string) (string, error) {
	if endpoint == "" {
		endpoint = getOllamaEndpoint()
	}
	if model == "" {
		model = defaultModel
	}

	fromLang := getLangName(fromCode)
	toLang := getLangName(toCode)

	fmt.Printf("[TRANSLATE] Starting translation: %s -> %s\n", fromCode, toCode)
	fmt.Printf("[TRANSLATE] Endpoint: %s | Model: %s | Text length: %d chars\n", endpoint, model, len(text))

	const maxAttempts = 3
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		translated, err := requestTranslation(text, fromLang, toLang, endpoint, model, attempt > 1)
		if err != nil {
			lastErr = err
			fmt.Printf("[TRANSLATE] attempt %d/%d error: %v\n", attempt, maxAttempts, err)
			continue
		}
		if !looksLikeTargetLang(toCode, translated) {
			lastErr = fmt.Errorf("translation result is not in target language %q (model returned wrong script)", toCode)
			fmt.Printf("[TRANSLATE] attempt %d/%d rejected: output not in %s script\n", attempt, maxAttempts, toCode)
			continue
		}
		fmt.Printf("[TRANSLATE] SUCCESS: %s -> %s (%d chars, attempt %d)\n", fromCode, toCode, len(translated), attempt)
		return translated, nil
	}
	return "", lastErr
}

// requestTranslation performs a single Ollama chat call and returns the cleaned
// translation. When strict is true it uses a more forceful prompt that names the
// target language and forbids replying in the source language — used on retry
// after the first attempt came back in the wrong script.
func requestTranslation(text, fromLang, toLang, endpoint, model string, strict bool) (string, error) {
	systemPrompt := `You are a professional translator. Your task is to translate text accurately and naturally.
Always respond with ONLY the translated text, nothing else. No explanations, no markdown, no tags.
Just the clean translation.`
	userPrompt := fmt.Sprintf("Translate from %s to %s:\n\n%s", fromLang, toLang, text)

	if strict {
		systemPrompt = fmt.Sprintf(`You are a professional translator translating into %s.
CRITICAL: Write the translation ONLY in %s using its native script.
Do NOT reply in %s. Do NOT keep any %s characters.
Respond with ONLY the translated text — no explanations, no markdown, no tags.`, toLang, toLang, fromLang, fromLang)
		userPrompt = fmt.Sprintf("Translate the following text into %s. Output only %s:\n\n%s", toLang, toLang, text)
	}

	return chat(systemPrompt, userPrompt, endpoint, model)
}

// chat performs a single OpenAI-compatible chat call and returns the cleaned
// assistant content with <think>...</think> reasoning blocks (emitted by
// Deepseek-R1 and compatible models) stripped.
func chat(systemPrompt, userPrompt, endpoint, model string) (string, error) {
	// Normalize to an OpenAI-compatible chat-completions endpoint. Settings often
	// store just the base URL (e.g. ".../compatible-mode/v1"); append the path so
	// hosted providers like DashScope don't 404.
	if endpoint == "" {
		endpoint = getOllamaEndpoint()
	}
	if !strings.Contains(endpoint, "/chat/completions") {
		endpoint = strings.TrimRight(endpoint, "/") + "/chat/completions"
	}

	req := ChatRequest{
		Model: model,
		Messages: []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Stream: false,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// OpenAI-compatible endpoints that require auth (e.g. DashScope, OpenAI,
	// DeepSeek cloud) read a bearer token from OLLAMA_API_KEY. Plain local
	// Ollama leaves it unset and the header is omitted.
	if key := strings.TrimSpace(os.Getenv("OLLAMA_API_KEY")); key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("ollama connection error: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	var chatResp ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("no response from translation model")
	}

	out := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	out = strings.TrimSpace(reThinkBlock.ReplaceAllString(out, ""))
	if out == "" {
		return "", fmt.Errorf("empty response from translation model")
	}
	return out, nil
}

// reFieldMarker matches the "@@@FIELD:key@@@" delimiter lines used to bundle
// several fields into one translation request.
var reFieldMarker = regexp.MustCompile(`(?m)^[ \t]*@@@FIELD:([A-Za-z0-9_]+)@@@[ \t]*$`)

// TranslateFields translates several named fields in a SINGLE model call, then
// splits the reply back per field.
//
// Why bundle: a short, proper-noun-heavy field (e.g. a product title/name)
// translated on its own gives a weak model too little context — it tends to echo
// the source language or mistranslate (observed: a Chinese title rendered into
// Chinese/garbled for ar/th even though the standalone description translated
// fine). Sending the title together with the longer description lets the title
// borrow that context, which markedly improves quality.
//
// Each split-out field is validated against the target script; any field that is
// missing from the combined reply, comes back in the wrong language, or appears
// hallucinated/corrupted falls back to an individual translation (which itself
// retries with a stricter prompt).
func TranslateFields(fields map[string]string, fromCode, toCode, endpoint, model string) (map[string]string, error) {
	if endpoint == "" {
		endpoint = getOllamaEndpoint()
	}
	if model == "" {
		model = defaultModel
	}

	// Keep only non-empty fields, in a stable (alphabetical) order so that
	// "description" precedes "name" — the title is translated with the
	// description already in context.
	keys := make([]string, 0, len(fields))
	for k := range fields {
		if strings.TrimSpace(fields[k]) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	out := make(map[string]string, len(keys))
	if len(keys) == 0 {
		return out, nil
	}

	// Combined call is only worthwhile with 2+ fields; keep validated results.
	if len(keys) >= 2 {
		for k, v := range translateCombined(keys, fields, fromCode, toCode, endpoint, model) {
			// Validate: target script, field length sanity, no mixed-script hallucination
			if looksLikeTargetLang(toCode, v) && fieldLengthSane(fields[k], v, k) && !hasMixedScripts(toCode, v) {
				out[k] = v
			}
		}
	}

	// Per-field fallback for anything still missing or in the wrong language.
	var firstErr error
	for _, k := range keys {
		if _, ok := out[k]; ok {
			continue
		}
		t, err := translateViaOpenAI(fields[k], fromCode, toCode, endpoint, model)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out[k] = t
	}
	if len(out) == 0 {
		return out, firstErr
	}
	return out, nil
}

// translateCombined sends all fields in one delimited request and parses the
// reply back into a field→text map. It returns whatever it can fully parse
// (every requested key present) or nil; callers validate and fill gaps.
func translateCombined(keys []string, fields map[string]string, fromCode, toCode, endpoint, model string) map[string]string {
	fromLang := getLangName(fromCode)
	toLang := getLangName(toCode)

	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, "@@@FIELD:%s@@@\n%s\n", k, strings.TrimSpace(fields[k]))
	}

	systemPrompt := fmt.Sprintf(`You are a professional translator. Translate into %s.
The input has sections, each introduced by a line of the form @@@FIELD:key@@@.
Reproduce every @@@FIELD:key@@@ marker line EXACTLY and keep the same order.
Translate ONLY the text under each marker into %s using its native script.
Do NOT reply in %s. Output only the markers and the translated text — no explanations, no markdown, no tags.`, toLang, toLang, fromLang)
	userPrompt := fmt.Sprintf("Translate the sections below into %s:\n\n%s", toLang, sb.String())

	const maxAttempts = 2
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, err := chat(systemPrompt, userPrompt, endpoint, model)
		if err != nil {
			fmt.Printf("[TRANSLATE] combined %s->%s attempt %d error: %v\n", fromCode, toCode, attempt, err)
			continue
		}
		parsed := parseFields(resp)
		complete := true
		for _, k := range keys {
			if _, ok := parsed[k]; !ok {
				complete = false
				break
			}
		}
		if complete {
			fmt.Printf("[TRANSLATE] combined %s->%s OK (%d fields, attempt %d)\n", fromCode, toCode, len(keys), attempt)
			return parsed
		}
		fmt.Printf("[TRANSLATE] combined %s->%s attempt %d: incomplete markers (%d/%d)\n", fromCode, toCode, attempt, len(parsed), len(keys))
	}
	return nil
}

// parseFields splits a combined translation response on @@@FIELD:key@@@ markers
// into a field→text map.
func parseFields(resp string) map[string]string {
	locs := reFieldMarker.FindAllStringSubmatchIndex(resp, -1)
	res := make(map[string]string, len(locs))
	for i, m := range locs {
		key := resp[m[2]:m[3]]
		valStart := m[1]
		valEnd := len(resp)
		if i+1 < len(locs) {
			valEnd = locs[i+1][0]
		}
		val := strings.TrimSpace(resp[valStart:valEnd])
		if key != "" && val != "" {
			res[key] = val
		}
	}
	return res
}

// fieldLengthSane checks whether a translated field length is reasonable relative
// to the source. Allows 3x expansion for titles, 2x for descriptions (to catch
// egregious hallucinations that bloat short fields into lengthy text).
func fieldLengthSane(src, dst string, fieldName string) bool {
	srcLen := len(strings.TrimSpace(src))
	dstLen := len(strings.TrimSpace(dst))
	if srcLen == 0 {
		return dstLen == 0
	}
	// Title fields should not expand much; descriptions can expand more
	maxRatio := 3.0
	if fieldName == "description" {
		maxRatio = 2.0
	}
	return float64(dstLen)/float64(srcLen) <= maxRatio
}

// hasMixedScripts detects hallucination by checking if translated text contains
// an unrelated script alongside the target script. Even a single character from
// an unexpected script is suspicious (indicates model hallucination/corruption).
func hasMixedScripts(toCode, text string) bool {
	// Only check languages where script mixing is a serious concern
	checks := map[string][]func(rune) bool{
		"ar": {isHan, isThai, isCyrillic}, // Detect Chinese, Thai, Cyrillic in Arabic
		"th": {isHan, isArabic, isCyrillic}, // Detect Chinese, Arabic, Cyrillic in Thai
	}
	badScripts, ok := checks[toCode]
	if !ok {
		return false // No mixed-script check for this language
	}

	// Any character from an unrelated script = hallucination
	for _, r := range text {
		if unicode.IsLetter(r) {
			for _, badPred := range badScripts {
				if badPred(r) {
					return true // Found even one character from unrelated script
				}
			}
		}
	}
	return false
}

// scriptCheck maps a target language code to a predicate that reports whether a
// rune belongs to that language's native script. Latin-based languages (en, es,
// fr, de, pt) and Japanese (Han overlaps with Chinese, so it can't be told apart
// reliably) are intentionally omitted — their results are not script-validated.
var scriptCheck = map[string]func(rune) bool{
	"ar": isArabic,
	"th": isThai,
	"ru": isCyrillic,
	"ko": isHangul,
	"cn": isHan,
}

func isArabic(r rune) bool {
	return (r >= 0x0600 && r <= 0x06FF) || (r >= 0x0750 && r <= 0x077F) ||
		(r >= 0x08A0 && r <= 0x08FF) || (r >= 0xFB50 && r <= 0xFDFF) ||
		(r >= 0xFE70 && r <= 0xFEFF)
}

func isThai(r rune) bool { return r >= 0x0E00 && r <= 0x0E7F }

func isCyrillic(r rune) bool { return (r >= 0x0400 && r <= 0x04FF) || (r >= 0x0500 && r <= 0x052F) }

func isHangul(r rune) bool {
	return (r >= 0xAC00 && r <= 0xD7A3) || (r >= 0x1100 && r <= 0x11FF) ||
		(r >= 0x3130 && r <= 0x318F)
}

func isHan(r rune) bool { return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF) }

// looksLikeTargetLang reports whether text plausibly contains the script
// expected for toCode. Languages without a registered script check always pass.
// At least 20% of the letters must be in the expected script: this reliably
// rejects wrong-language output (e.g. Chinese returned for an Arabic request,
// where the in-script ratio is ~0) while tolerating embedded Latin brand names,
// model numbers, and punctuation.
func looksLikeTargetLang(toCode, text string) bool {
	pred, ok := scriptCheck[toCode]
	if !ok {
		return true
	}
	var inScript, letters int
	for _, r := range text {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if pred(r) {
			inScript++
		}
	}
	if letters == 0 {
		return false
	}
	return inScript*100 >= letters*20
}
