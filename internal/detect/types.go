// Package detect implements Tier-1 regex/validator and Tier-2 LLM NER detection.
package detect

// Category classifies a detected sensitive span.
type Category string

const (
	CatEmail            Category = "EMAIL"
	CatPhone            Category = "PHONE"
	CatSSN              Category = "SSN"
	CatCreditCard       Category = "CREDIT_CARD"
	CatAWSKey           Category = "AWS_KEY"
	CatGCPKey           Category = "GCP_KEY"
	CatGitHubToken      Category = "GITHUB_TOKEN"
	CatSlackToken       Category = "SLACK_TOKEN"
	CatOpenAIKey        Category = "OPENAI_KEY"
	CatStripeKey        Category = "STRIPE_KEY"
	CatJWT              Category = "JWT"
	CatPrivateKey       Category = "PRIVATE_KEY"
	CatIP               Category = "IP"
	CatMAC              Category = "MAC"
	CatPerson           Category = "PERSON"
	CatOrg              Category = "ORG"
	CatAddress          Category = "ADDRESS"
	CatProjectCodename  Category = "PROJECT_CODENAME"
	CatInternalHostname Category = "INTERNAL_HOSTNAME"
	CatMedical          Category = "MEDICAL"
	CatFinancial        Category = "FINANCIAL"
	CatOtherID          Category = "OTHER_ID"
	CatHighEntropy      Category = "HIGH_ENTROPY"
	CatURLCredential    Category = "URL_CREDENTIAL"
	CatWatchlist        Category = "WATCHLIST"
)

// PrefixFor returns the placeholder prefix for a category.
func PrefixFor(c Category) string {
	switch c {
	case CatEmail:
		return "EMAIL"
	case CatPhone:
		return "PHONE"
	case CatSSN:
		return "SSN"
	case CatCreditCard:
		return "CARD"
	case CatAWSKey, CatGCPKey, CatGitHubToken, CatSlackToken, CatOpenAIKey, CatStripeKey, CatHighEntropy:
		return "KEY"
	case CatJWT:
		return "JWT"
	case CatPrivateKey:
		return "PKEY"
	case CatIP:
		return "IP"
	case CatMAC:
		return "MAC"
	case CatPerson:
		return "PERSON"
	case CatOrg:
		return "ORG"
	case CatAddress:
		return "ADDR"
	case CatProjectCodename, CatWatchlist:
		return "CODE"
	case CatInternalHostname:
		return "HOST"
	case CatMedical:
		return "MED"
	case CatFinancial:
		return "FIN"
	case CatURLCredential:
		return "URLCRED"
	default:
		return "ID"
	}
}

// IsSecret reports whether the category is a high-severity secret.
func IsSecret(c Category) bool {
	switch c {
	case CatAWSKey, CatGCPKey, CatGitHubToken, CatSlackToken, CatOpenAIKey,
		CatStripeKey, CatPrivateKey, CatCreditCard, CatHighEntropy, CatURLCredential:
		return true
	default:
		return false
	}
}

// Finding is a single detected span.
type Finding struct {
	Text       string   `json:"text"`
	Category   Category `json:"category"`
	Start      int      `json:"start"`
	End        int      `json:"end"`
	Confidence float64  `json:"confidence"`
	Tier       int      `json:"tier"` // 1 or 2
}

// Result is the full detection output for a text blob.
type Result struct {
	Findings []Finding `json:"findings"`
	Latency  int64     `json:"latency_ms"`
}
