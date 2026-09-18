package models

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRepoDescriptor_JSONSerialization(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	desc := RepoDescriptor{
		Title:           "Enterprise Linux 9 BaseOS",
		Format:          "rpm",
		Arch:            "x86_64",
		Distro:          "el9",
		Channel:         "base",
		PackageCount:    1420,
		LastBuild:       now,
		BaseURL:         "https://repo.example.com/el9/base/x86_64/",
		PortalURL:       "../../../index.html",
		RepoviewVersion: "0.2.0",
	}

	data, err := json.Marshal(desc)
	if err != nil {
		t.Fatalf("failed to marshal RepoDescriptor: %v", err)
	}

	var decoded RepoDescriptor
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal RepoDescriptor: %v", err)
	}

	if decoded.Title != desc.Title {
		t.Errorf("expected Title %q, got %q", desc.Title, decoded.Title)
	}
	if decoded.Format != desc.Format {
		t.Errorf("expected Format %q, got %q", desc.Format, decoded.Format)
	}
	if decoded.Arch != desc.Arch {
		t.Errorf("expected Arch %q, got %q", desc.Arch, decoded.Arch)
	}
	if decoded.Distro != desc.Distro {
		t.Errorf("expected Distro %q, got %q", desc.Distro, decoded.Distro)
	}
	if decoded.Channel != desc.Channel {
		t.Errorf("expected Channel %q, got %q", desc.Channel, decoded.Channel)
	}
	if decoded.PackageCount != desc.PackageCount {
		t.Errorf("expected PackageCount %d, got %d", desc.PackageCount, decoded.PackageCount)
	}
	if !decoded.LastBuild.Equal(desc.LastBuild) {
		t.Errorf("expected LastBuild %v, got %v", desc.LastBuild, decoded.LastBuild)
	}
	if decoded.BaseURL != desc.BaseURL {
		t.Errorf("expected BaseURL %q, got %q", desc.BaseURL, decoded.BaseURL)
	}
	if decoded.PortalURL != desc.PortalURL {
		t.Errorf("expected PortalURL %q, got %q", desc.PortalURL, decoded.PortalURL)
	}
	if decoded.RepoviewVersion != desc.RepoviewVersion {
		t.Errorf("expected RepoviewVersion %q, got %q", desc.RepoviewVersion, decoded.RepoviewVersion)
	}
}
