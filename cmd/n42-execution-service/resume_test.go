package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

func TestResumeOwnerMetadataRejectsAliasesAndSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "metadata")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if raw, err := readResumeMetadata(file, 7); err != nil || string(raw) != "fixture" {
		t.Fatalf("private metadata: %q %v", raw, err)
	}
	if _, err := readResumeMetadata(file, 6); err == nil {
		t.Fatal("oversized metadata accepted")
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(file, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := readResumeMetadata(alias, 100); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(file, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := readResumeMetadata(file, 100); err == nil {
		t.Fatal("multiple hard links accepted")
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readResumeMetadata(fifo, 100); err == nil {
		t.Fatal("FIFO metadata accepted")
	}
}

func TestResumeOwnerFlagsRejectIncompleteModeBeforeDatabaseOpen(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "not-created")
	base := []string{"--genesis", filepath.Join(dir, "missing-genesis"), "--data-dir", data}
	for _, extra := range [][]string{
		{"--strict-ingress-manifest", "/tmp/manifest", "--resume-ingress-plan", "/tmp/plan"},
		{"--resume-ingress-plan", "/tmp/plan"},
		{"--resume-ingress-source", "/tmp/source"},
		{"--resume-ingress-plan-id", "0x00"},
		{"--ingress-max-transactions", "1417"},
	} {
		err := run(context.Background(), append(append([]string(nil), base...), extra...))
		if err == nil || (!strings.Contains(err.Error(), "resume") && !strings.Contains(err.Error(), "ingress")) {
			t.Fatalf("invalid resume mode not rejected at input gate: %v", err)
		}
		if _, err := os.Lstat(data); !os.IsNotExist(err) {
			t.Fatal("rejected mode created runtime data")
		}
	}
}

func TestResumeOwnerOptionsSelectBoundedLocalInputsWithoutAuthorizingPlan(t *testing.T) {
	dir := t.TempDir()
	planID := types.Hash{1}
	// A document parser is intentionally not an authorizer: the real owner
	// must reject this synthetic plan during independent startup verification.
	doc := sharedexec.ResumePlanDocument{Kind: "gov5-resume-four-frame-plan-v1", PlanID: planID, Plan: sharedexec.ResumePlan{Senders: []sharedexec.ResumeSender{{}}, Frames: make([]sharedexec.ResumeFrame, 4)}}
	plan, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{"plan": plan, "proof": []byte("synthetic audit content"), "manifest": ingressManifestForTest(t)} {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	opts, err := resumeIngressOptions(filepath.Join(dir, "plan"), filepath.Join(dir, "proof"), dir, filepath.Join(dir, "manifest"), planID.Hex(), 0, 0)
	if err != nil || opts == nil || opts.ExpectedPlanID != planID || opts.SourceRoot != dir || opts.MaxTransactions != 1417 || opts.MaxEncodedBytes != 1<<20 || opts.GenesisRaw != nil {
		t.Fatalf("explicit resume options: %+v %v", opts, err)
	}
	if opts, err := resumeIngressOptions("", "", "", "", "", 0, 0); err != nil || opts != nil {
		t.Fatal("default owner selected resume ingress")
	}
	if _, err := resumeIngressOptions(filepath.Join(dir, "plan"), filepath.Join(dir, "proof"), dir, filepath.Join(dir, "manifest"), (types.Hash{2}).Hex(), 0, 0); err == nil {
		t.Fatal("locally mismatched expected plan ID accepted")
	}
}
