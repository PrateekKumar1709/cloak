package detect

import (
	"math"
	"net"
	"regexp"
	"strings"
	"unicode"
)

type rule struct {
	category Category
	re       *regexp.Regexp
	validate func(string) bool
	conf     float64
}

var (
	reEmail = regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b`)
	rePhone = regexp.MustCompile(`(?:\+?1[-.\s]?)?(?:\(?\d{3}\)?[-.\s]?)\d{3}[-.\s]?\d{4}\b`)
	// Whisper / ASR often emits bare digit runs (10–11 digits)
	rePhoneDigits = regexp.MustCompile(`\b(?:\+?1[2-9]\d{9}|[2-9]\d{9,10})\b`)
	reSSN   = regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)
	reCC    = regexp.MustCompile(`\b(?:\d[ -]*?){13,19}\b`)
	reAWS   = regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)
	reAWSsk = regexp.MustCompile(`(?i)(?:aws_secret_access_key|secret_access_key)\s*[=:]\s*['"]?([A-Za-z0-9/+=]{40})['"]?`)
	reGCP   = regexp.MustCompile(`"type"\s*:\s*"service_account"`)
	reGH    = regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9_]{36,}\b`)
	reSlack = regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`)
	reOAI   = regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}\b`)
	reStripe = regexp.MustCompile(`\bsk_(?:live|test)_[A-Za-z0-9]{20,}\b`)
	reJWT   = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)
	rePKEY  = regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----[\s\S]+?-----END (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----`)
	reIPv4  = regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|[01]?\d\d?)\.){3}(?:25[0-5]|2[0-4]\d|[01]?\d\d?)\b`)
	reIPv6  = regexp.MustCompile(`\b(?:[0-9a-fA-F]{1,4}:){2,7}[0-9a-fA-F]{1,4}\b`)
	reMAC   = regexp.MustCompile(`\b(?:[0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}\b`)
	reURLCred = regexp.MustCompile(`(?i)\b(?:https?|ftp|postgres|mysql|mongodb|redis)://[^:\s]+:[^@\s]+@[^\s]+`)
	reIBAN  = regexp.MustCompile(`\b[A-Z]{2}\d{2}[A-Z0-9]{11,30}\b`)
	reTokenish = regexp.MustCompile(`\b[A-Za-z0-9_\-+/=]{32,}\b`)
)

func rules() []rule {
	return []rule{
		{CatPrivateKey, rePKEY, nil, 1.0},
		{CatURLCredential, reURLCred, nil, 1.0},
		{CatAWSKey, reAWS, nil, 1.0},
		{CatGitHubToken, reGH, nil, 1.0},
		{CatSlackToken, reSlack, nil, 1.0},
		{CatStripeKey, reStripe, nil, 1.0},
		{CatOpenAIKey, reOAI, nil, 0.95},
		{CatJWT, reJWT, nil, 0.95},
		{CatEmail, reEmail, nil, 1.0},
		{CatSSN, reSSN, validSSN, 0.95},
		{CatCreditCard, reCC, luhnValid, 0.95},
		{CatPhone, rePhone, validPhone, 0.85},
		{CatPhone, rePhoneDigits, validPhone, 0.8},
		{CatIP, reIPv4, validIP, 0.9},
		{CatIP, reIPv6, validIP, 0.9},
		{CatMAC, reMAC, nil, 0.9},
		{CatFinancial, reIBAN, nil, 0.85},
	}
}

// Tier1 scans text with deterministic detectors.
func Tier1(text string) []Finding {
	var out []Finding
	seen := map[string]bool{}

	// AWS secret key line pattern (capture group)
	for _, m := range reAWSsk.FindAllStringSubmatchIndex(text, -1) {
		if len(m) >= 4 {
			start, end := m[2], m[3]
			val := text[start:end]
			key := CatAWSKey.String() + "|" + val
			if !seen[key] {
				seen[key] = true
				out = append(out, Finding{Text: val, Category: CatAWSKey, Start: start, End: end, Confidence: 1.0, Tier: 1})
			}
		}
	}

	for _, r := range rules() {
		idxs := r.re.FindAllStringIndex(text, -1)
		for _, idx := range idxs {
			start, end := idx[0], idx[1]
			val := text[start:end]
			if r.validate != nil && !r.validate(val) {
				continue
			}
			key := string(r.category) + "|" + val
			if seen[key] {
				continue
			}
			// skip emails that look like keys overlapping
			if r.category == CatPhone && looksLikePartOfLargerNumber(text, start, end) {
				continue
			}
			seen[key] = true
			out = append(out, Finding{
				Text: val, Category: r.category, Start: start, End: end,
				Confidence: r.conf, Tier: 1,
			})
		}
	}

	// High-entropy token-like spans (skip if already matched)
	for _, idx := range reTokenish.FindAllStringIndex(text, -1) {
		start, end := idx[0], idx[1]
		val := text[start:end]
		if seen["|"+val] || entropy(val) < 4.2 {
			continue
		}
		// skip if already covered by another finding
		covered := false
		for _, f := range out {
			if start >= f.Start && end <= f.End {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		// skip emails / urls fragments
		if strings.Contains(val, "@") || strings.Contains(val, "://") {
			continue
		}
		key := string(CatHighEntropy) + "|" + val
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Finding{
			Text: val, Category: CatHighEntropy, Start: start, End: end,
			Confidence: 0.7, Tier: 1,
		})
	}

	return out
}

// WatchlistFindings finds configured watchlist strings (case-insensitive).
func WatchlistFindings(text string, watchlist []string) []Finding {
	var out []Finding
	lower := strings.ToLower(text)
	for _, w := range watchlist {
		if w == "" {
			continue
		}
		wl := strings.ToLower(w)
		idx := 0
		for {
			pos := strings.Index(lower[idx:], wl)
			if pos < 0 {
				break
			}
			start := idx + pos
			end := start + len(w)
			// preserve original casing from source
			out = append(out, Finding{
				Text: text[start:end], Category: CatWatchlist,
				Start: start, End: end, Confidence: 1.0, Tier: 1,
			})
			idx = end
		}
	}
	return out
}

func (c Category) String() string { return string(c) }

func luhnValid(s string) bool {
	var digits []int
	for _, r := range s {
		if unicode.IsDigit(r) {
			digits = append(digits, int(r-'0'))
		}
	}
	n := len(digits)
	if n < 13 || n > 19 {
		return false
	}
	sum := 0
	alt := false
	for i := n - 1; i >= 0; i-- {
		d := digits[i]
		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
	}
	return sum%10 == 0
}

func validSSN(s string) bool {
	parts := strings.Split(s, "-")
	if len(parts) != 3 {
		return false
	}
	if parts[0] == "000" || parts[0] == "666" || (parts[0] >= "900" && parts[0] <= "999") {
		return false
	}
	if parts[1] == "00" || parts[2] == "0000" {
		return false
	}
	return true
}

func validPhone(s string) bool {
	digits := 0
	for _, r := range s {
		if unicode.IsDigit(r) {
			digits++
		}
	}
	return digits == 10 || digits == 11
}

func validIP(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	// skip obvious non-PII locals optionally; still flag private for INTERNAL awareness
	return true
}

func looksLikePartOfLargerNumber(text string, start, end int) bool {
	if start > 0 && unicode.IsDigit(rune(text[start-1])) {
		return true
	}
	if end < len(text) && unicode.IsDigit(rune(text[end])) {
		return true
	}
	return false
}

func entropy(s string) float64 {
	if len(s) == 0 {
		return 0
	}
	freq := map[rune]float64{}
	for _, r := range s {
		freq[r]++
	}
	var h float64
	n := float64(len(s))
	for _, c := range freq {
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}
