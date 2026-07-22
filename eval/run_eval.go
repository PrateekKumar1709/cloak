// Eval harness for Cloak detectors: precision/recall + latency.
//
//	go run ./eval -fixtures eval/fixtures/dev_leaks.jsonl
//	go run ./eval -fixtures eval/fixtures/dev_leaks.jsonl -tier2  # needs Lemonade
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/PrateekKumar1709/cloak/internal/config"
	"github.com/PrateekKumar1709/cloak/internal/detect"
	"github.com/PrateekKumar1709/cloak/internal/lemonade"
)

type caseRow struct {
	ID     string   `json:"id"`
	Text   string   `json:"text"`
	Expect []string `json:"expect"`
}

type catStats struct {
	TP, FP, FN int
	Latencies  []int64
}

func main() {
	fixtures := flag.String("fixtures", "eval/fixtures/dev_leaks.jsonl", "JSONL fixture path")
	tier2 := flag.Bool("tier2", false, "enable Lemonade Tier-2 NER")
	outJSON := flag.String("json", "", "optional results JSON path")
	flag.Parse()

	cfg := config.Default()
	var t2 *detect.Tier2Runner
	if *tier2 {
		client := lemonade.New(cfg.Lemonade.BaseURL, cfg.Lemonade.Model, 30*time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := client.Healthy(ctx)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "lemonade not healthy: %v\n", err)
			os.Exit(1)
		}
		t2 = &detect.Tier2Runner{Client: client, Timeout: time.Duration(cfg.Lemonade.TimeoutMS) * time.Millisecond}
		fmt.Printf("Tier-2 enabled via %s model=%s\n", cfg.Lemonade.BaseURL, cfg.Lemonade.Model)
	}

	pipe := detect.NewPipeline(detect.PipelineConfig{
		Watchlist:          []string{"Project Nightingale"},
		Allowlist:          nil,
		Tier2FailOpenSoft:  true,
		CacheMessageHashes: false,
	}, t2)

	f, err := os.Open(*fixtures)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open fixtures: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	byCat := map[string]*catStats{}
	var allLat []int64
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var row caseRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			fmt.Fprintf(os.Stderr, "bad row: %v\n", err)
			continue
		}
		n++
		res := pipe.Scan(context.Background(), row.Text)
		allLat = append(allLat, res.Latency)
		got := map[string]bool{}
		for _, fnd := range res.Findings {
			got[string(fnd.Category)] = true
			st := byCat[string(fnd.Category)]
			if st == nil {
				st = &catStats{}
				byCat[string(fnd.Category)] = st
			}
			st.Latencies = append(st.Latencies, res.Latency)
		}
		expect := map[string]bool{}
		for _, e := range row.Expect {
			expect[e] = true
			if byCat[e] == nil {
				byCat[e] = &catStats{}
			}
		}
		for e := range expect {
			if got[e] {
				byCat[e].TP++
			} else {
				byCat[e].FN++
			}
		}
		for g := range got {
			if !expect[g] {
				// only count FP for categories we care about in expect set of any case
				// soft: skip unknown extras as FP only if category is in global expect universe
				if isScored(g) {
					byCat[g].FP++
				}
			}
		}
	}

	fmt.Printf("\nCloak eval: %d cases (tier2=%v)\n", n, *tier2)
	fmt.Println(strings.Repeat("-", 72))
	fmt.Printf("%-22s %8s %8s %8s %8s %8s\n", "category", "P", "R", "F1", "support", "p50_ms")
	cats := make([]string, 0, len(byCat))
	for c := range byCat {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	results := map[string]any{}
	for _, c := range cats {
		st := byCat[c]
		p, r, f1 := prf(st.TP, st.FP, st.FN)
		support := st.TP + st.FN
		p50 := percentile(st.Latencies, 0.5)
		fmt.Printf("%-22s %8.2f %8.2f %8.2f %8d %8.0f\n", c, p, r, f1, support, p50)
		results[c] = map[string]any{"precision": p, "recall": r, "f1": f1, "support": support, "p50_ms": p50}
	}
	fmt.Println(strings.Repeat("-", 72))
	fmt.Printf("overall detection latency  p50=%.0fms  p95=%.0fms\n",
		percentile(allLat, 0.5), percentile(allLat, 0.95))
	results["_meta"] = map[string]any{
		"cases": n, "tier2": *tier2,
		"p50_ms": percentile(allLat, 0.5), "p95_ms": percentile(allLat, 0.95),
	}
	if *outJSON != "" {
		b, _ := json.MarshalIndent(results, "", "  ")
		_ = os.WriteFile(*outJSON, b, 0o644)
		fmt.Printf("wrote %s\n", *outJSON)
	}
}

func isScored(cat string) bool {
	switch cat {
	case "EMAIL", "PHONE", "SSN", "CREDIT_CARD", "AWS_KEY", "GITHUB_TOKEN",
		"SLACK_TOKEN", "OPENAI_KEY", "STRIPE_KEY", "PERSON", "ORG",
		"INTERNAL_HOSTNAME", "PROJECT_CODENAME", "WATCHLIST", "URL_CREDENTIAL",
		"IP", "JWT", "PRIVATE_KEY":
		return true
	default:
		return false
	}
}

func prf(tp, fp, fn int) (p, r, f1 float64) {
	if tp+fp > 0 {
		p = float64(tp) / float64(tp+fp)
	}
	if tp+fn > 0 {
		r = float64(tp) / float64(tp+fn)
	}
	if p+r > 0 {
		f1 = 2 * p * r / (p + r)
	}
	return
}

func percentile(vals []int64, p float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]int64(nil), vals...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	idx := int(float64(len(cp)-1) * p)
	return float64(cp[idx])
}
