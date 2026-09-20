package main

// The bounds on what a manifest, which nobody signs, can ask a consumer to
// download, unpack and believe.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// manifestEditedBy returns the packed manifest with its archive object changed
// by edit.
func manifestEditedBy(t *testing.T, good []byte, edit func(archive map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(good, &m); err != nil {
		t.Fatal(err)
	}
	edit(m["archive"].(map[string]any))
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestManifestBoundsWhatItCanAskAConsumerFor(t *testing.T) {
	f := newFixture(t)
	good := mustRead(t, f.manifest)
	total := f.m.Archive.UncompressedSize
	const gib = int64(1) << 30

	cases := []struct {
		name string
		edit func(a map[string]any)
		want string // empty: the manifest is accepted
	}{
		{"an archive of the largest size", func(a map[string]any) { a["size"] = 64 * gib }, ""},
		{"an archive larger than any snapshot may be", func(a map[string]any) { a["size"] = 64*gib + 1 }, "above the"},
		{"an absurd archive", func(a map[string]any) { a["size"] = int64(1) << 60 }, "above the"},
		{"a database that unpacks to more than any snapshot may", func(a map[string]any) {
			files := a["files"].([]any)
			files[0].(map[string]any)["size"] = 65 * gib
			a["uncompressedSize"] = 65*gib + total - int64(f.m.Archive.Files[0].Size)
			a["size"] = 64 * gib
		}, "a snapshot may unpack to"},
		{"the most an archive may expand", func(a map[string]any) { a["size"] = (total + 63) / 64 }, ""},
		{"an archive that expands a little more", func(a map[string]any) { a["size"] = (total+63)/64 - 1 }, "decompression bomb"},
		{"a 1 MiB archive of a gigabyte of zeros", func(a map[string]any) {
			files := a["files"].([]any)
			files[0].(map[string]any)["size"] = gib
			a["uncompressedSize"] = gib + total - int64(f.m.Archive.Files[0].Size)
			a["size"] = int64(1) << 20
		}, "decompression bomb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseManifest(manifestEditedBy(t, good, tc.edit))
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("the manifest was refused: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("expected an error containing %q, got: %v", tc.want, err)
			}
		})
	}
	// What packing makes is within the bounds by a wide margin (it is what the
	// producer would otherwise be unable to publish).
	if ratio := float64(total) / float64(f.m.Archive.Size); ratio > 8 {
		t.Fatalf("a snapshot of a chain database expands %.1f times: the expansion bound would refuse real snapshots", ratio)
	}
}

// A consistent archive of one allowed-name file of zeros is what an archive that
// fills a disk looks like: a few kilobytes on the wire, and the manifest says it
// is 64 MiB. It is refused for what it says before a byte of it is unpacked.
func TestExtractRefusesADecompressionBombBeforeWritingAnything(t *testing.T) {
	f := newFixture(t)
	const big = 64 << 20
	current := []byte("MANIFEST-000001\n")
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "CURRENT", Size: int64(len(current)), Mode: 0o644}))
	_, err := tw.Write(current)
	must(err)
	must(tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "MANIFEST-000001", Size: big, Mode: 0o644}))
	zero := make([]byte, 1<<20)
	sum := sha256.New()
	for i := 0; i < big>>20; i++ {
		_, err := tw.Write(zero)
		must(err)
		sum.Write(zero)
	}
	must(tw.Close())
	must(gz.Close())
	currentSum := sha256.Sum256(current)
	manifestPath, archivePath := f.hostile("bomb", raw.Bytes(), func(m *manifest) {
		m.Archive.Files = []fileEntry{
			{Name: "CURRENT", Size: int64(len(current)), Sha256: hex.EncodeToString(currentSum[:])},
			{Name: "MANIFEST-000001", Size: big, Sha256: hex.EncodeToString(sum.Sum(nil))},
		}
		m.Archive.UncompressedSize = big + int64(len(current))
	})
	if size := int64(raw.Len()); size*64 > big {
		t.Fatalf("the test's archive (%d bytes) is not a bomb of %d bytes", size, big)
	}
	target := f.t.TempDir() + "/data"
	err = f.extractFrom(manifestPath, archivePath, target)
	if err == nil || !strings.Contains(err.Error(), "decompression bomb") {
		t.Fatalf("expected the archive to be refused as a decompression bomb, got: %v", err)
	}
	mustNotLeaveTraces(t, target)
	var out, errOut bytes.Buffer
	code := run([]string{"verify", "--manifest", manifestPath, "--archive", archivePath, "--chain-id", fmt.Sprint(f.chainID), "--genesis-hash", hex0x(f.genesis)}, &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "decompression bomb") {
		t.Fatalf("verify accepted the bomb (exit %d): %s", code, errOut.String())
	}
}

// verify reads (and hashes) every byte an archive unpacks to, so it is bounded
// like extract is.
func TestVerifyHonoursTheSizeCeiling(t *testing.T) {
	f := newFixture(t)
	pins := []string{"--manifest", f.manifest, "--archive", f.archive, "--chain-id", fmt.Sprint(f.chainID), "--genesis-hash", hex0x(f.genesis)}
	var out, errOut bytes.Buffer
	if code := run(append([]string{"verify"}, pins...), &out, &errOut); code != 0 {
		t.Fatalf("verify without a ceiling: exit %d: %s", code, errOut.String())
	}
	errOut.Reset()
	code := run(append(append([]string{"verify"}, pins...), "--max-bytes", "1024"), &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "above the allowed") {
		t.Fatalf("verify accepted a manifest announcing more than --max-bytes (exit %d): %s", code, errOut.String())
	}
	out.Reset()
	if code := run([]string{"manifest", "show", "--manifest", f.manifest, "--field", "archive.uncompressedSize"}, &out, &errOut); code != 0 || strings.TrimSpace(out.String()) != strconv.FormatInt(f.m.Archive.UncompressedSize, 10) {
		t.Fatalf("exit %d: %q", code, out.String())
	}
}

// A snapshot cannot hold a block that does not exist yet; a little clock skew is
// not held against it.
func TestExtractToleratesClockSkewButNotAnInventedFuture(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ahead  time.Duration
		accept bool
	}{
		{"a tip a minute ahead", time.Minute, true},
		{"a tip an hour ahead", time.Hour, false},
		{"a tip ten minutes ahead", 10 * time.Minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, expect := forgedSnapshot(t, time.Now().Add(tc.ahead).Unix())
			target := f.t.TempDir() + "/data"
			_, _, err := extractSnapshot(extractOptions{ManifestPath: f.manifest, ArchivePath: f.archive, Target: target, Expect: expect, Now: time.Now()})
			if tc.accept && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !tc.accept && (err == nil || !strings.Contains(err.Error(), "ahead of this host's clock")) {
				t.Fatalf("expected a refusal for the tip's date, got: %v", err)
			}
		})
	}
}
