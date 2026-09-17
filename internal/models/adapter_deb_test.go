package models

import (
	"testing"

	"pault.ag/go/debian/control"
	"pault.ag/go/debian/dependency"
	"pault.ag/go/debian/version"
)

func TestPackageFromBinaryIndex(t *testing.T) {
	ver, err := version.Parse("2:1.22.1-9ubuntu1")
	if err != nil {
		t.Fatalf("failed to parse test version: %v", err)
	}

	idx := control.BinaryIndex{
		Paragraph: control.Paragraph{
			Values: map[string]string{
				"Package":        "nginx",
				"Version":        "2:1.22.1-9ubuntu1",
				"Architecture":   "amd64",
				"Maintainer":     "Ubuntu Developers <ubuntu-devel-discuss@lists.ubuntu.com>",
				"Installed-Size": "1354",
				"Depends":        "libc6 (>= 2.34), libpcre2-8-0 (>= 10.22)",
				"Pre-Depends":    "init-system-helpers (>= 1.54~)",
				"Recommends":     "logrotate",
				"Suggests":       "nginx-doc",
				"Conflicts":      "nginx-core",
				"Breaks":         "nginx-light (<< 1.20)",
				"Replaces":       "nginx-common",
				"Section":        "httpd",
				"Homepage":       "https://nginx.org",
				"Filename":       "pool/main/n/nginx/nginx_1.22.1-9ubuntu1_amd64.deb",
				"Size":           "524108",
				"SHA256":         "abcdef1234567890",
				"Description":    "small, powerful, scalable web/proxy server\n Nginx is a web server which also acts as a reverse proxy.",
			},
		},
		Package:       "nginx",
		Version:       ver,
		Architecture:  dependency.Arch{CPU: "amd64", OS: "linux"},
		Maintainer:    "Ubuntu Developers <ubuntu-devel-discuss@lists.ubuntu.com>",
		InstalledSize: 1354,
		Section:       "httpd",
		Homepage:      "https://nginx.org",
		Filename:      "pool/main/n/nginx/nginx_1.22.1-9ubuntu1_amd64.deb",
		Size:          524108,
		SHA256:        "abcdef1234567890",
		Description:   "small, powerful, scalable web/proxy server\n Nginx is a web server which also acts as a reverse proxy.",
	}

	pkg := PackageFromBinaryIndex(idx)

	if pkg.Format != FormatDEB {
		t.Errorf("expected FormatDEB, got %v", pkg.Format)
	}
	if pkg.Name != "nginx" {
		t.Errorf("expected 'nginx', got %q", pkg.Name)
	}
	if pkg.Version != "1.22.1" {
		t.Errorf("expected '1.22.1', got %q", pkg.Version)
	}
	if pkg.Release != "9ubuntu1" {
		t.Errorf("expected '9ubuntu1', got %q", pkg.Release)
	}
	if pkg.Epoch != "2" {
		t.Errorf("expected epoch '2', got %q", pkg.Epoch)
	}
	if pkg.Summary != "small, powerful, scalable web/proxy server" {
		t.Errorf("unexpected summary: %q", pkg.Summary)
	}
	if pkg.InstalledSize != 1354*1024 {
		t.Errorf("expected installed size %d, got %d", 1354*1024, pkg.InstalledSize)
	}
	if pkg.SizePackage != 524108 {
		t.Errorf("expected size 524108, got %d", pkg.SizePackage)
	}

	// Dependencies check
	if pkg.Dependencies == nil {
		t.Fatalf("expected non-nil dependencies")
	}
	// Requires should have Pre-Depends + Depends
	if len(pkg.Dependencies.Requires) != 3 {
		t.Errorf("expected 3 requires (1 pre + 2 dep), got %d", len(pkg.Dependencies.Requires))
	}
	// Recommends
	if len(pkg.Dependencies.Recommends) != 1 || pkg.Dependencies.Recommends[0].Name != "logrotate" {
		t.Errorf("unexpected recommends: %+v", pkg.Dependencies.Recommends)
	}
	// Suggests
	if len(pkg.Dependencies.Suggests) != 1 || pkg.Dependencies.Suggests[0].Name != "nginx-doc" {
		t.Errorf("unexpected suggests: %+v", pkg.Dependencies.Suggests)
	}
	// Conflicts + Breaks
	if len(pkg.Dependencies.Conflicts) != 2 {
		t.Errorf("expected 2 conflicts (1 conflict + 1 break), got %d", len(pkg.Dependencies.Conflicts))
	}
	// Obsoletes (Replaces)
	if len(pkg.Dependencies.Obsoletes) != 1 || pkg.Dependencies.Obsoletes[0].Name != "nginx-common" {
		t.Errorf("unexpected obsoletes: %+v", pkg.Dependencies.Obsoletes)
	}
}
