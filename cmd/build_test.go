package cmd

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/antoniocascais/motoko/pkg/config"
	"github.com/antoniocascais/motoko/pkg/vm"
)

type buildStub struct {
	exists    bool
	existsErr error
	refs      []vm.GoldenRef
	refsErr   error
	buildErr  error

	refsCalls  int
	buildCalls int
	refsArgs   [2]string
	buildCfg   vm.BuildGoldenConfig
}

// stubBuild swaps the build seams for fakes driven by s. Originals restored via t.Cleanup.
func stubBuild(t *testing.T, s *buildStub) {
	t.Helper()
	origExists, origRefs, origBuild := goldenExistsFn, goldenReferencesFn, buildGoldenFn
	goldenExistsFn = func(string, string) (bool, error) { return s.exists, s.existsErr }
	goldenReferencesFn = func(dir, name string) ([]vm.GoldenRef, error) {
		s.refsCalls++
		s.refsArgs = [2]string{dir, name}
		return s.refs, s.refsErr
	}
	buildGoldenFn = func(c vm.BuildGoldenConfig) error {
		s.buildCalls++
		s.buildCfg = c
		return s.buildErr
	}
	t.Cleanup(func() {
		goldenExistsFn, goldenReferencesFn, buildGoldenFn = origExists, origRefs, origBuild
	})
}

func TestRunBuild_DecisionTable(t *testing.T) {
	oneRef := []vm.GoldenRef{{Path: "/img/motoko-a-overlay.qcow2"}}
	errScan := errors.New("scan failed")
	errBuild := errors.New("bake failed")

	tests := []struct {
		name       string
		stub       buildStub
		force      bool
		wantInUse  bool
		wantErr    error
		wantRefs   int
		wantBuilds int
	}{
		{name: "no golden builds", stub: buildStub{}, wantRefs: 1, wantBuilds: 1},
		{name: "no golden with force builds", stub: buildStub{}, force: true, wantRefs: 1, wantBuilds: 1},
		{name: "no golden but referenced refuses", stub: buildStub{refs: oneRef}, wantInUse: true, wantRefs: 1},
		{name: "no golden scan error refuses", stub: buildStub{refsErr: errScan}, wantErr: errScan, wantRefs: 1},
		{name: "exists without force is a no-op", stub: buildStub{exists: true}},
		{name: "exists force unreferenced rebuilds", stub: buildStub{exists: true}, force: true, wantRefs: 1, wantBuilds: 1},
		{name: "exists force referenced refuses", stub: buildStub{exists: true, refs: oneRef}, force: true, wantInUse: true, wantRefs: 1},
		{name: "exists force scan error refuses", stub: buildStub{exists: true, refsErr: errScan}, force: true, wantErr: errScan, wantRefs: 1},
		{name: "build error propagates", stub: buildStub{buildErr: errBuild}, wantErr: errBuild, wantRefs: 1, wantBuilds: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.stub
			stubBuild(t, &s)
			cfg := config.DefaultConfig()

			var err error
			captureStderr(t, func() { err = runBuild(cfg, tt.force) })

			var inUse *vm.GoldenInUseError
			switch {
			case tt.wantInUse:
				if !errors.As(err, &inUse) {
					t.Fatalf("err = %v, want *GoldenInUseError", err)
				}
				if len(inUse.Refs) != len(tt.stub.refs) {
					t.Errorf("error carries %d refs, want %d", len(inUse.Refs), len(tt.stub.refs))
				}
				if want := filepath.Join(cfg.ImagesDir, cfg.GoldenImage.Name); inUse.Golden != want {
					t.Errorf("error names %q, want %q", inUse.Golden, want)
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
			case err != nil:
				t.Fatalf("unexpected error: %v", err)
			}
			if s.refsCalls != tt.wantRefs {
				t.Errorf("reference scans = %d, want %d", s.refsCalls, tt.wantRefs)
			}
			if s.buildCalls != tt.wantBuilds {
				t.Errorf("builds = %d, want %d", s.buildCalls, tt.wantBuilds)
			}
		})
	}
}

func TestRunBuild_ExistsCheckError(t *testing.T) {
	errStat := errors.New("stat failed")
	s := buildStub{existsErr: errStat}
	stubBuild(t, &s)

	if err := runBuild(config.DefaultConfig(), true); !errors.Is(err, errStat) {
		t.Fatalf("err = %v, want %v", err, errStat)
	}
	if s.refsCalls != 0 || s.buildCalls != 0 {
		t.Errorf("scans=%d builds=%d, want 0/0", s.refsCalls, s.buildCalls)
	}
}

// Every config field must reach the scan and the build; a swapped or dropped
// field would scan or bake the wrong image.
func TestRunBuild_PassesConfig(t *testing.T) {
	s := buildStub{}
	stubBuild(t, &s)
	cfg := config.DefaultConfig()
	cfg.ImagesDir = "/imgs"
	cfg.VMUser = "agent"
	cfg.BaseImage = config.BaseImage{URL: "https://example.com/b.qcow2", Checksum: "sha512:ab", Filename: "b.qcow2"}
	cfg.GoldenImage.Name = "g.qcow2"
	cfg.GoldenImage.RootDiskGB = 13
	cfg.GoldenImage.Packages = []string{"jq"}
	cfg.GoldenImage.ClaudeChannel = "latest"
	cfg.GoldenImage.ClaudeVersion = "2.1.310"

	var err error
	captureStderr(t, func() { err = runBuild(cfg, false) })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if want := [2]string{"/imgs", "g.qcow2"}; s.refsArgs != want {
		t.Errorf("scan args = %v, want %v", s.refsArgs, want)
	}
	want := vm.BuildGoldenConfig{
		ImagesDir:     "/imgs",
		BaseURL:       "https://example.com/b.qcow2",
		BaseChecksum:  "sha512:ab",
		BaseFilename:  "b.qcow2",
		GoldenName:    "g.qcow2",
		RootDiskGB:    13,
		Packages:      []string{"jq"},
		VMUser:        "agent",
		ClaudeChannel: "latest",
		ClaudeVersion: "2.1.310",
	}
	if !reflect.DeepEqual(s.buildCfg, want) {
		t.Errorf("build config = %+v\nwant           %+v", s.buildCfg, want)
	}
}
