package app

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/edsilegxrepo/repoview/internal/state"
)

type memoryOutputSink struct {
	mu    sync.Mutex
	files map[string][]byte
}

func newMemoryOutputSink() *memoryOutputSink {
	return &memoryOutputSink{files: make(map[string][]byte)}
}

func (m *memoryOutputSink) WriteFile(relPath string, content []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]byte, len(content))
	copy(cp, content)
	m.files[relPath] = cp
	return nil
}

func (m *memoryOutputSink) RemoveFile(relPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, relPath)
	return nil
}

func (m *memoryOutputSink) Exists(relPath string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.files[relPath]
	return ok
}

func createXMLMockRPMRepo(t *testing.T, repoDir string) {
	t.Helper()
	repodata := filepath.Join(repoDir, "repodata")
	if err := os.MkdirAll(repodata, 0o755); err != nil {
		t.Fatal(err)
	}
	writeGZ := func(name, content string) {
		f, err := os.Create(filepath.Join(repodata, name))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		gz := gzip.NewWriter(f)
		_, _ = gz.Write([]byte(content))
		_ = gz.Close()
	}

	writeGZ("primary.xml.gz", `<?xml version="1.0" encoding="UTF-8"?>
<metadata xmlns="http://linux.duke.edu/metadata/common" xmlns:rpm="http://linux.duke.edu/metadata/rpm" packages="1">
  <package type="rpm">
    <name>curl</name><arch>x86_64</arch><version epoch="0" ver="8.0.1" rel="1.el9"/>
    <checksum type="sha256" pkgid="YES">abc123</checksum>
    <summary>Transfer data from or to a server</summary>
    <description>curl command line tool</description>
    <packager>BuildBot</packager>
    <time file="1700000100" build="1700000000"/>
    <size package="1024" installed="2048" archive="3072"/>
    <location href="Packages/c/curl-8.0.1-1.el9.x86_64.rpm"/>
    <format><rpm:license>MIT</rpm:license><rpm:group>Applications/Internet</rpm:group></format>
  </package>
</metadata>`)
	writeGZ("other.xml.gz", `<?xml version="1.0" encoding="UTF-8"?>
<otherdata xmlns="http://linux.duke.edu/metadata/other" packages="1">
  <package pkgid="abc123" name="curl" arch="x86_64"><version epoch="0" ver="8.0.1" rel="1.el9"/>
    <changelog author="Dev &lt;dev@example.com&gt;" date="1700000000">- Initial build</changelog>
  </package>
</otherdata>`)
	writeGZ("updateinfo.xml.gz", `<?xml version="1.0" encoding="UTF-8"?>
<updates><update type="security" version="2"><id>ELSA-2026:9999</id><title>Security fix</title><severity>Moderate</severity><issued date="2026-02-01"/>
  <references><reference type="cve" id="CVE-2026-9999" href="https://example.com/CVE-2026-9999"/></references>
  <pkglist><collection><package name="curl" version="8.0.1" release="1.el9" epoch="0" arch="x86_64"/></collection></pkglist>
</update></updates>`)
	repomd := `<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <revision>r42</revision>
  <data type="primary"><location href="repodata/primary.xml.gz"/></data>
  <data type="other"><location href="repodata/other.xml.gz"/></data>
  <data type="updateinfo"><location href="repodata/updateinfo.xml.gz"/></data>
</repomd>`
	if err := os.WriteFile(filepath.Join(repodata, "repomd.xml"), []byte(repomd), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRepoConfigFindAndLoad(t *testing.T) {
	dir := t.TempDir()
	yamlText := `title: Custom YAML Title
description: Repo description
suite: noble
arch: amd64
workers: 4
ignore_package:
  - "*-debuginfo"
exclude_arch:
  - i686
`
	if err := os.WriteFile(filepath.Join(dir, "repoview.yaml"), []byte(yamlText), 0o644); err != nil {
		t.Fatal(err)
	}
	foundPath, ok := FindRepoConfig(dir)
	if !ok {
		t.Fatalf("expected FindRepoConfig to locate repoview.yaml")
	}
	rc, err := LoadRepoConfig(foundPath)
	if err != nil {
		t.Fatalf("LoadRepoConfig failed: %v", err)
	}
	if rc.Title != "Custom YAML Title" || rc.Suite != "noble" || rc.Arch != "amd64" || rc.Workers != 4 {
		t.Errorf("unexpected parsed RepoConfig: %+v", rc)
	}
	if len(rc.IgnorePackage) != 1 || len(rc.ExcludeArch) != 1 {
		t.Errorf("expected ignore_package and exclude_arch slices populated, got %+v", rc)
	}
}

func TestGenerator_FingerprintShortCircuitAndStdoutSink(t *testing.T) {
	repoDir := t.TempDir()
	outDir := filepath.Join(t.TempDir(), "out")
	createXMLMockRPMRepo(t, repoDir)

	var stdout1, stderr1 bytes.Buffer
	cfg := Config{
		RepoDir:   repoDir,
		OutputDir: outDir,
		Title:     "XML Security Repo",
		Version:   "0.2.0",
		Stdout:    &stdout1,
		Stderr:    &stderr1,
	}
	if err := NewGenerator(cfg).Run(); err != nil {
		t.Fatalf("first Generator.Run failed: %v", err)
	}

	// Verify state.json stores repository fingerprint and rendered curl page has CVE-2026-9999
	st, err := state.NewStateStore(filepath.Join(outDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fp, ok := st.Get(state.MetadataKeyRepoFingerprint); !ok || fp == "" {
		t.Errorf("expected state.json to persist %s", state.MetadataKeyRepoFingerprint)
	}
	pkgHTML, err := os.ReadFile(filepath.Join(outDir, "curl.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pkgHTML), "CVE-2026-9999") {
		t.Errorf("expected rendered curl.html to include CVE-2026-9999 advisory")
	}

	// Second run with unchanged inputs should short-circuit before opening SQLite/XML metadata
	var stdout2, stderr2 bytes.Buffer
	cfg2 := cfg
	cfg2.Stdout = &stdout2
	cfg2.Stderr = &stderr2
	if err := NewGenerator(cfg2).Run(); err != nil {
		t.Fatalf("second Generator.Run failed: %v", err)
	}
	if !strings.Contains(stdout2.String(), "skipping regeneration") {
		t.Errorf("expected incremental fingerprint short-circuit message on Stdout, got: %q", stdout2.String())
	}

	// Custom OutputSink test
	sink := newMemoryOutputSink()
	sinkOut := filepath.Join(t.TempDir(), "sink-state")
	var stdout3 bytes.Buffer
	cfgSink := Config{
		RepoDir:    repoDir,
		OutputDir:  filepath.Join(sinkOut, "html"),
		StateDir:   sinkOut,
		Title:      "Sink Repo",
		Version:    "0.2.0",
		OutputSink: sink,
		Stdout:     &stdout3,
	}
	if err := NewGenerator(cfgSink).Run(); err != nil {
		t.Fatalf("OutputSink Generator.Run failed: %v", err)
	}
	if !sink.Exists("index.html") || !sink.Exists("curl.html") || !sink.Exists("search.json") {
		t.Errorf("expected OutputSink to receive index.html, curl.html, and search.json")
	}
}

func TestGenerator_RepoYAMLRun_CleanupStale_AndErrors(t *testing.T) {
	repoDir := t.TempDir()
	outDir := filepath.Join(repoDir, "repoview")
	createXMLMockRPMRepo(t, repoDir)

	// Place repoview.yaml directly inside repoDir without specifying CLI Title
	yamlBody := "title: Native YAML Repo\ndescription: Loaded automatically inside Generator.Run\ndistro: el9\nchannel: appstream\n"
	if err := os.WriteFile(filepath.Join(repoDir, "repoview.yaml"), []byte(yamlBody), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := NewGenerator(Config{RepoDir: repoDir, OutputDir: outDir, Stdout: &stdout, Stderr: &stderr}).Run(); err != nil {
		t.Fatalf("Generator.Run with repoview.yaml failed: %v", err)
	}
	idxBytes, err := os.ReadFile(filepath.Join(outDir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(idxBytes), "Native YAML Repo") {
		t.Errorf("expected Native YAML Repo from repoview.yaml inside index.html")
	}

	// Verify cleanupStale deletes removed artifact from disk & stateStore
	st, err := state.NewStateStore(filepath.Join(outDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	staleFile := filepath.Join(outDir, "obsolete.html")
	if err := os.WriteFile(staleFile, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	st.MarkWritten("obsolete.html", "oldhash")
	g := NewGenerator(Config{RepoDir: repoDir, OutputDir: outDir, Stdout: &stdout, Stderr: &stderr})
	g.cleanupStale(nil, st, []string{"index.html"})
	if _, err := os.Stat(staleFile); !os.IsNotExist(err) {
		t.Errorf("expected obsolete.html to be deleted by cleanupStale")
	}

	// Verify SafetyError & PartialFailureError error wrapping
	se := &SafetyError{Msg: "blocked"}
	if se.Error() != "blocked" || se.Unwrap() != ErrSafety {
		t.Errorf("unexpected SafetyError: %q / %v", se.Error(), se.Unwrap())
	}
	pe := &PartialFailureError{ErrorCount: 2}
	if !strings.Contains(pe.Error(), "2 package errors") {
		t.Errorf("unexpected PartialFailureError: %q", pe.Error())
	}
}
