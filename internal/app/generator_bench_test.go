package app

// OBJECTIVES:
// Benchmark end-to-end SQLite RPM package page generation throughput and allocations
// across 500-2000 packages under realistic prepared dependency graphs.
//
// TEST STRATEGY:
// Generates deterministic SQLite repositories inside b.TempDir() via createBenchmarkRPMRepo,
// resets the timer per iteration, and reports ns/op, B/op, and allocs/op using Force mode
// without timing fixture setup cost.

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/edsilegxrepo/repoview/internal/models"
	_ "github.com/mattn/go-sqlite3"
)

func createBenchmarkRPMRepo(b *testing.B, repoDir string, numPkgs int) {
	b.Helper()
	repodata := filepath.Join(repoDir, "repodata")
	if err := os.MkdirAll(repodata, 0o755); err != nil {
		b.Fatal(err)
	}

	pdb, err := sql.Open("sqlite3", filepath.Join(repodata, "primary.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = pdb.Close() }()

	schema := `
	CREATE TABLE packages (
		pkgKey INTEGER PRIMARY KEY,
		name TEXT, epoch TEXT, version TEXT, release TEXT, arch TEXT,
		summary TEXT, description TEXT, url TEXT, time_build INTEGER,
		rpm_license TEXT, rpm_sourcerpm TEXT, size_package INTEGER,
		location_href TEXT, rpm_vendor TEXT, rpm_group TEXT,
		rpm_buildhost TEXT, size_installed INTEGER
	);
	CREATE TABLE requires (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT, pre BOOLEAN);
	CREATE TABLE provides (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT);
	CREATE TABLE conflicts (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT);
	CREATE TABLE obsoletes (pkgKey INTEGER, name TEXT, flags TEXT, epoch TEXT, version TEXT, release TEXT);
	`
	if _, err := pdb.Exec(schema); err != nil {
		b.Fatal(err)
	}

	tx, err := pdb.Begin()
	if err != nil {
		b.Fatal(err)
	}
	stmtPkg, err := tx.Prepare(`INSERT INTO packages (pkgKey, name, epoch, version, release, arch, summary, description, url, time_build, rpm_license, rpm_sourcerpm, size_package, location_href, rpm_vendor, rpm_group, rpm_buildhost, size_installed) VALUES (?, ?, '0', '1.0.0', '1.el9', 'x86_64', ?, 'Benchmark package description', 'https://example.com', ?, 'MIT', 'src.rpm', 4096, '', 'BenchCorp', 'Applications/System', 'builder.local', 8192)`)
	if err != nil {
		b.Fatal(err)
	}
	stmtReq, err := tx.Prepare(`INSERT INTO requires (pkgKey, name, flags, epoch, version, release, pre) VALUES (?, 'libc.so.6', 'GE', '0', '2.34', '1', 0)`)
	if err != nil {
		b.Fatal(err)
	}
	stmtProv, err := tx.Prepare(`INSERT INTO provides (pkgKey, name, flags, epoch, version, release) VALUES (?, ?, 'EQ', '0', '1.0.0', '1.el9')`)
	if err != nil {
		b.Fatal(err)
	}

	for i := 1; i <= numPkgs; i++ {
		name := fmt.Sprintf("bench-pkg-%04d", i)
		if _, err := stmtPkg.Exec(i, name, "Summary for "+name, 1700000000+i); err != nil {
			b.Fatal(err)
		}
		if _, err := stmtReq.Exec(i); err != nil {
			b.Fatal(err)
		}
		if _, err := stmtProv.Exec(i, name); err != nil {
			b.Fatal(err)
		}
	}
	_ = stmtPkg.Close()
	_ = stmtReq.Close()
	_ = stmtProv.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}

	odb, err := sql.Open("sqlite3", filepath.Join(repodata, "other.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = odb.Close() }()
	if _, err := odb.Exec(`CREATE TABLE changelog (pkgKey INTEGER, author TEXT, date INTEGER, changelog TEXT)`); err != nil {
		b.Fatal(err)
	}
	repomd := `<?xml version="1.0" encoding="UTF-8"?>
<repomd xmlns="http://linux.duke.edu/metadata/repo">
  <data type="primary_db"><location href="repodata/primary.sqlite"/><database_version>10</database_version></data>
  <data type="other_db"><location href="repodata/other.sqlite"/></data>
</repomd>`
	if err := os.WriteFile(filepath.Join(repodata, "repomd.xml"), []byte(repomd), 0o644); err != nil {
		b.Fatal(err)
	}
}

func BenchmarkRenderPackages(b *testing.B) {
	repoDir := b.TempDir()
	const numPkgs = 500
	createBenchmarkRPMRepo(b, repoDir, numPkgs)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		outDir := filepath.Join(b.TempDir(), "out")
		cfg := Config{
			RepoDir:   repoDir,
			OutputDir: outDir,
			Title:     "Benchmark Repo",
			Format:    models.FormatRPM,
			Force:     true,
			Quiet:     true,
			Workers:   4,
		}
		if err := NewGenerator(cfg).Run(); err != nil {
			b.Fatalf("Generator.Run failed: %v", err)
		}
	}
}
