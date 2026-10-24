package vm

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// qemuInfoRunner fakes `qemu-img info` by path: infos maps an image path to its
// JSON output, fails names a path whose info call errors. Unlisted paths report
// no backing file. `cp` writes its destination so BuildGoldenImage can rename it.
func qemuInfoRunner(t *testing.T, infos map[string]string, fails map[string]bool) *[]execRecord {
	t.Helper()
	return mockRunnerWithFunc(t, func(name string, args ...string) (string, string, error) {
		switch name {
		case "cp":
			_ = os.WriteFile(args[len(args)-1], []byte("new golden"), 0644)
		case "qemu-img":
			if len(args) > 0 && args[0] == "info" {
				p := args[len(args)-1]
				if fails[p] {
					return "", "lock error", fmt.Errorf("qemu-img failed")
				}
				if out, ok := infos[p]; ok {
					return out, "", nil
				}
				return `{"format": "qcow2"}`, "", nil
			}
		}
		return "", "", nil
	})
}

func backingJSON(backing string) string {
	return fmt.Sprintf(`{"format": "qcow2", "backing-filename": %q}`, backing)
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
}

func refPaths(refs []GoldenRef) []string {
	var out []string
	for _, r := range refs {
		out = append(out, r.Path)
	}
	return out
}

// Overlays orphaned by a deleted golden still reference its path; recreating
// the golden there would silently rebase them, so they must still count.
func TestGoldenReferences_GoldenAbsent(t *testing.T) {
	dir := t.TempDir()
	golden := filepath.Join(dir, "golden.qcow2")
	orphan := filepath.Join(dir, "motoko-a-overlay.qcow2")
	relOrphan := filepath.Join(dir, "motoko-b-overlay.qcow2")
	otherBacked := filepath.Join(dir, "motoko-c-overlay.qcow2")
	unreadable := filepath.Join(dir, "motoko-d-overlay.qcow2")
	unrelated := filepath.Join(dir, "motoko-e-data.qcow2")
	uncleanOrphan := filepath.Join(dir, "motoko-f-overlay.qcow2")
	for _, p := range []string{orphan, relOrphan, otherBacked, unreadable, unrelated, uncleanOrphan} {
		touch(t, p)
	}
	qemuInfoRunner(t, map[string]string{
		orphan:      backingJSON(golden),
		relOrphan:   backingJSON("golden.qcow2"),
		otherBacked: backingJSON(filepath.Join(dir, "golden-old.qcow2")),
		// Absolute and through a nonexistent dir: only path cleaning can match it.
		uncleanOrphan: backingJSON(dir + "/missing/../golden.qcow2"),
	}, map[string]bool{unreadable: true})

	refs, err := GoldenReferences(dir, "golden.qcow2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []GoldenRef{
		{Path: orphan},
		{Path: relOrphan},
		{Path: unreadable, Unverified: true},
		{Path: uncleanOrphan},
	}
	if !reflect.DeepEqual(refs, want) {
		t.Errorf("refs = %+v\nwant  %+v", refs, want)
	}
}

func TestGoldenReferences_GoldenAbsentNoRefs(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "motoko-a-data.qcow2"))
	qemuInfoRunner(t, nil, nil)

	refs, err := GoldenReferences(dir, "golden.qcow2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("refs = %v, want none", refs)
	}
}

func TestGoldenReferences_Classification(t *testing.T) {
	dir := t.TempDir()
	golden := filepath.Join(dir, "golden.qcow2")
	other := filepath.Join(dir, "golden-old.qcow2")
	link := filepath.Join(dir, "golden-link")
	touch(t, golden)
	touch(t, other)
	if err := os.Symlink(golden, link); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}

	absRef := filepath.Join(dir, "motoko-abs-overlay.qcow2")
	relRef := filepath.Join(dir, "motoko-rel-overlay.qcow2")
	dotRef := filepath.Join(dir, "motoko-dot-overlay.qcow2")
	dotdotRef := filepath.Join(dir, "motoko-dotdot-overlay.qcow2")
	symRef := filepath.Join(dir, "motoko-sym-overlay.qcow2")
	fullRef := filepath.Join(dir, "motoko-full-overlay.qcow2")
	otherBacked := filepath.Join(dir, "motoko-old-overlay.qcow2")
	missingBacked := filepath.Join(dir, "motoko-gone-overlay.qcow2")
	dataDisk := filepath.Join(dir, "motoko-a-data.qcow2")
	unreadable := filepath.Join(dir, "motoko-locked-overlay.qcow2")
	for _, p := range []string{absRef, relRef, dotRef, dotdotRef, symRef, fullRef, otherBacked, missingBacked, dataDisk, unreadable} {
		touch(t, p)
	}

	qemuInfoRunner(t, map[string]string{
		absRef:        backingJSON(golden),
		relRef:        backingJSON("golden.qcow2"),
		dotRef:        backingJSON("./golden.qcow2"),
		dotdotRef:     backingJSON("sub/../golden.qcow2"),
		symRef:        backingJSON(link),
		fullRef:       `{"backing-filename": "unused", "full-backing-filename": "` + golden + `"}`,
		otherBacked:   backingJSON(other),
		missingBacked: backingJSON(filepath.Join(dir, "deleted.qcow2")),
	}, map[string]bool{unreadable: true})

	refs, err := GoldenReferences(dir, "golden.qcow2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// ReadDir returns entries sorted by name.
	want := []GoldenRef{
		{Path: absRef},
		{Path: dotRef},
		{Path: dotdotRef},
		{Path: fullRef},
		{Path: unreadable, Unverified: true},
		{Path: relRef},
		{Path: symRef},
	}
	if !reflect.DeepEqual(refs, want) {
		t.Errorf("refs = %+v\nwant  %+v", refs, want)
	}
}

func TestGoldenReferences_SkipsNonImages(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "golden.qcow2"))
	touch(t, filepath.Join(dir, ".golden-bake-123.qcow2"))
	touch(t, filepath.Join(dir, "motoko-a-cloud-init.iso"))
	touch(t, filepath.Join(dir, "notes.txt"))
	if err := os.Mkdir(filepath.Join(dir, "sub.qcow2"), 0755); err != nil {
		t.Fatal(err)
	}
	recs := qemuInfoRunner(t, nil, nil)

	refs, err := GoldenReferences(dir, "golden.qcow2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(refs) != 0 {
		t.Errorf("refs = %v, want none", refs)
	}
	// The golden itself, hidden bakes, non-qcow2 files and dirs are never inspected.
	if len(*recs) != 0 {
		t.Errorf("expected no qemu-img calls, got %v", *recs)
	}
}

func TestGoldenReferences_ExactArgs(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "golden.qcow2"))
	overlay := filepath.Join(dir, "motoko-a-overlay.qcow2")
	touch(t, overlay)
	recs := qemuInfoRunner(t, nil, nil)

	if _, err := GoldenReferences(dir, "golden.qcow2"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []execRecord{{name: "qemu-img", args: []string{"info", "-U", "--output=json", overlay}}}
	if !reflect.DeepEqual(*recs, want) {
		t.Errorf("calls = %v, want %v", *recs, want)
	}
}

func TestGoldenReferences_UnparseableInfoIsUnverified(t *testing.T) {
	for _, tt := range []struct{ name, out string }{
		{"not json", "not json"},
		{"empty output", ""},
		{"backing not a string", `{"backing-filename": 42}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			touch(t, filepath.Join(dir, "golden.qcow2"))
			overlay := filepath.Join(dir, "motoko-a-overlay.qcow2")
			touch(t, overlay)
			qemuInfoRunner(t, map[string]string{overlay: tt.out}, nil)

			refs, err := GoldenReferences(dir, "golden.qcow2")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := []GoldenRef{{Path: overlay, Unverified: true}}
			if !reflect.DeepEqual(refs, want) {
				t.Errorf("refs = %+v, want %+v", refs, want)
			}
		})
	}
}

func TestGoldenReferences_GoldenStatErrorIsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(dir, "golden.qcow2"))
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0755) })
	qemuInfoRunner(t, nil, nil)

	// A golden that cannot be stat'ed must not be mistaken for an absent one,
	// which would report "no references" and permit replacement.
	refs, err := GoldenReferences(dir, "golden.qcow2")
	if err == nil {
		t.Fatalf("expected error, got refs %v", refs)
	}
}

// lockListing makes dir enterable but not listable: the golden can still be
// stat'ed, but ReadDir fails. Restored via t.Cleanup.
func lockListing(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0311); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0755) })
}

// An images dir that cannot be listed must fail the scan, never report "no
// references" and permit replacement.
func TestGoldenReferences_UnlistableDirIsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	for _, goldenPresent := range []bool{true, false} {
		t.Run(fmt.Sprintf("golden present %v", goldenPresent), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "images")
			if err := os.Mkdir(dir, 0755); err != nil {
				t.Fatal(err)
			}
			if goldenPresent {
				touch(t, filepath.Join(dir, "golden.qcow2"))
			}
			touch(t, filepath.Join(dir, "motoko-a-overlay.qcow2"))
			lockListing(t, dir)
			qemuInfoRunner(t, nil, nil)

			refs, err := GoldenReferences(dir, "golden.qcow2")
			if err == nil || !strings.Contains(err.Error(), "reading images dir") {
				t.Fatalf("err = %v (refs %v), want reading images dir error", err, refs)
			}
		})
	}
}

func TestGoldenInUseError_Message(t *testing.T) {
	err := &GoldenInUseError{
		Golden: "/img/golden.qcow2",
		Refs:   []GoldenRef{{Path: "/img/a.qcow2"}, {Path: "/img/b.qcow2", Unverified: true}},
	}
	msg := err.Error()
	for _, want := range []string{
		"/img/golden.qcow2 is the backing file of 2 image(s)",
		"\n  /img/a.qcow2\n",
		"/img/b.qcow2 (backing file unreadable, assumed in use)",
		"golden_image.name",
		"motoko rebuild",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "/img/a.qcow2 (backing") {
		t.Errorf("verified ref must not be marked unreadable:\n%s", msg)
	}
}

func goldenBuildConfig(dir, checksum string) BuildGoldenConfig {
	return BuildGoldenConfig{
		ImagesDir:     dir,
		BaseURL:       "https://example.com/images/base.qcow2",
		BaseChecksum:  checksum,
		BaseFilename:  "base.qcow2",
		GoldenName:    "golden.qcow2",
		RootDiskGB:    8,
		VMUser:        "claude",
		ClaudeChannel: "stable",
	}
}

func assertNoBakeLeft(t *testing.T, dir string) {
	t.Helper()
	if left, _ := filepath.Glob(filepath.Join(dir, ".golden-bake-*")); len(left) > 0 {
		t.Errorf("temp bake not cleaned up: %v", left)
	}
}

func TestBuildGoldenImage_RefusesToReplaceReferencedGolden(t *testing.T) {
	dir := t.TempDir()
	checksum := writeBaseImage(t, dir)
	golden := filepath.Join(dir, "golden.qcow2")
	original := []byte("old golden")
	if err := os.WriteFile(golden, original, 0444); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "motoko-a-overlay.qcow2")
	touch(t, overlay)
	qemuInfoRunner(t, map[string]string{overlay: backingJSON(golden)}, nil)

	err := BuildGoldenImage(goldenBuildConfig(dir, checksum))

	var inUse *GoldenInUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("err = %v, want *GoldenInUseError", err)
	}
	if got := refPaths(inUse.Refs); !reflect.DeepEqual(got, []string{overlay}) {
		t.Errorf("refs = %v, want [%s]", got, overlay)
	}
	got, readErr := os.ReadFile(golden)
	if readErr != nil || !bytes.Equal(got, original) {
		t.Errorf("golden modified: %q (err %v)", got, readErr)
	}
	assertNoBakeLeft(t, dir)
}

func TestBuildGoldenImage_RefusesToRecreateMissingReferencedGolden(t *testing.T) {
	dir := t.TempDir()
	checksum := writeBaseImage(t, dir)
	golden := filepath.Join(dir, "golden.qcow2")
	orphan := filepath.Join(dir, "motoko-a-overlay.qcow2")
	touch(t, orphan)
	qemuInfoRunner(t, map[string]string{orphan: backingJSON(golden)}, nil)

	err := BuildGoldenImage(goldenBuildConfig(dir, checksum))

	var inUse *GoldenInUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("err = %v, want *GoldenInUseError", err)
	}
	if _, statErr := os.Stat(golden); !os.IsNotExist(statErr) {
		t.Errorf("golden recreated under orphaned overlay (stat err %v)", statErr)
	}
	assertNoBakeLeft(t, dir)
}

// A scan that fails after the bake must abort the replacement.
func TestBuildGoldenImage_ScanErrorKeepsExistingGolden(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	checksum := writeBaseImage(t, dir)
	golden := filepath.Join(dir, "golden.qcow2")
	original := []byte("old golden")
	if err := os.WriteFile(golden, original, 0444); err != nil {
		t.Fatal(err)
	}
	mockRunnerWithFunc(t, func(name string, args ...string) (string, string, error) {
		switch name {
		case "cp":
			_ = os.WriteFile(args[len(args)-1], []byte("new golden"), 0644)
		case "virt-customize":
			lockListing(t, dir)
		}
		return "", "", nil
	})

	err := BuildGoldenImage(goldenBuildConfig(dir, checksum))
	_ = os.Chmod(dir, 0755)

	if err == nil || !strings.Contains(err.Error(), "checking golden image references") {
		t.Fatalf("err = %v, want reference check error", err)
	}
	if got, _ := os.ReadFile(golden); !bytes.Equal(got, original) {
		t.Errorf("golden modified: %q", got)
	}
	assertNoBakeLeft(t, dir)
}

// An instance created while the bake runs must still block replacement: the
// reference check has to run after the bake, right before the rename.
func TestBuildGoldenImage_RechecksAfterBake(t *testing.T) {
	dir := t.TempDir()
	checksum := writeBaseImage(t, dir)
	golden := filepath.Join(dir, "golden.qcow2")
	original := []byte("old golden")
	if err := os.WriteFile(golden, original, 0444); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "motoko-late-overlay.qcow2")
	infos := map[string]string{}
	mockRunnerWithFunc(t, func(name string, args ...string) (string, string, error) {
		switch name {
		case "cp":
			_ = os.WriteFile(args[len(args)-1], []byte("new golden"), 0644)
		case "virt-customize":
			touch(t, overlay)
			infos[overlay] = backingJSON(golden)
		case "qemu-img":
			if args[0] == "info" {
				if out, ok := infos[args[len(args)-1]]; ok {
					return out, "", nil
				}
				return `{}`, "", nil
			}
		}
		return "", "", nil
	})

	err := BuildGoldenImage(goldenBuildConfig(dir, checksum))

	var inUse *GoldenInUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("err = %v, want *GoldenInUseError", err)
	}
	if got := refPaths(inUse.Refs); !reflect.DeepEqual(got, []string{overlay}) {
		t.Errorf("refs = %v, want [%s]", got, overlay)
	}
	if got, _ := os.ReadFile(golden); !bytes.Equal(got, original) {
		t.Errorf("golden modified: %q", got)
	}
	assertNoBakeLeft(t, dir)
}

func TestBuildGoldenImage_UnreadableOverlayBlocksReplacement(t *testing.T) {
	dir := t.TempDir()
	checksum := writeBaseImage(t, dir)
	golden := filepath.Join(dir, "golden.qcow2")
	if err := os.WriteFile(golden, []byte("old golden"), 0444); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(dir, "motoko-locked-overlay.qcow2")
	backed := filepath.Join(dir, "motoko-a-overlay.qcow2")
	touch(t, locked)
	touch(t, backed)
	qemuInfoRunner(t, map[string]string{backed: backingJSON(golden)}, map[string]bool{locked: true})

	err := BuildGoldenImage(goldenBuildConfig(dir, checksum))

	var inUse *GoldenInUseError
	if !errors.As(err, &inUse) {
		t.Fatalf("err = %v, want *GoldenInUseError", err)
	}
	want := []GoldenRef{{Path: backed}, {Path: locked, Unverified: true}}
	if !reflect.DeepEqual(inUse.Refs, want) {
		t.Errorf("refs = %+v, want %+v", inUse.Refs, want)
	}
	assertNoBakeLeft(t, dir)
}

func TestBuildGoldenImage_ReplacesUnreferencedGolden(t *testing.T) {
	dir := t.TempDir()
	checksum := writeBaseImage(t, dir)
	golden := filepath.Join(dir, "golden.qcow2")
	if err := os.WriteFile(golden, []byte("old golden"), 0444); err != nil {
		t.Fatal(err)
	}
	qemuInfoRunner(t, nil, nil)

	if err := BuildGoldenImage(goldenBuildConfig(dir, checksum)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new golden" {
		t.Errorf("golden content = %q, want the new bake", got)
	}
	info, err := os.Stat(golden)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0444 {
		t.Errorf("golden mode = %o, want 0444", info.Mode().Perm())
	}
	assertNoBakeLeft(t, dir)
}

func TestBuildGoldenImage_FailedBuildKeepsExistingGolden(t *testing.T) {
	dir := t.TempDir()
	checksum := writeBaseImage(t, dir)
	golden := filepath.Join(dir, "golden.qcow2")
	original := []byte("old golden")
	if err := os.WriteFile(golden, original, 0444); err != nil {
		t.Fatal(err)
	}
	mockRunnerWithFunc(t, func(name string, args ...string) (string, string, error) {
		switch name {
		case "cp":
			_ = os.WriteFile(args[len(args)-1], []byte("partial"), 0644)
		case "virt-customize":
			return "", "boom", fmt.Errorf("virt-customize failed")
		}
		return "", "", nil
	})

	if err := BuildGoldenImage(goldenBuildConfig(dir, checksum)); err == nil {
		t.Fatal("expected error")
	}
	got, err := os.ReadFile(golden)
	if err != nil || !bytes.Equal(got, original) {
		t.Errorf("golden modified after failed build: %q (err %v)", got, err)
	}
	assertNoBakeLeft(t, dir)
}
