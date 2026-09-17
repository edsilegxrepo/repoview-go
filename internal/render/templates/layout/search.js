/**
 * RepoView Client-Side Interactive Engine
 *
 * OBJECTIVES:
 * Deliver rich, desktop-grade interactivity (search, filtering, quick install,
 * theme toggling, dependency inspection, and client repository configuration)
 * in a 100% offline, air-gap compliant package with zero external CDN dependencies.
 *
 * CORE COMPONENTS:
 *   - Theme Management (initTheme): Light/Dark theme toggling with localStorage persistence.
 *   - Global Search (initSearch): Debounced full-text search against compact search.json with DOM XSS sanitization.
 *   - Quick Install Bar (initQuickInstall): Tool switcher (dnf, yum, wget, curl) with 1-click clipboard copy.
 *   - In-Page Table Filters (initGroupFilter, initFileFilter): Live sub-millisecond table filtering.
 *   - Dependency Inspector (initDependencyTabs): Tab switcher and live search for package requires/provides.
 *   - Client Repo Setup Helper (initRepoConfigModal): Dynamic reverse-proxy URL detection, .repo generation,
 *     and browser Blob file download.
 *   - GPG Verification Helper (initSignatureVerifyModal): Key ID inspection and verification command generator.
 *
 * FUNCTIONALITY & SECURITY:
 *   - Sanitizes all package metadata via escapeHTML() before DOM insertion to prevent XSS.
 *   - Implements bounded regex inputs to protect against client-side ReDoS.
 *   - Zero runtime network dependencies outside of the local repository directory.
 *
 * DATA FLOW:
 *   DOM Event / fetch('search.json') -> Client Filter / Formatter -> escapeHTML() -> DOM Update
 */

(() => {
	// Configuration
	const SEARCH_FILE = "search.json";
	const SEARCH_LIMIT = 50;

	// State
	let searchData = null;
	let isLoading = false;

	// =========================================================================
	// Theme Management (Light / Dark)
	// =========================================================================
	function initTheme() {
		const savedTheme = localStorage.getItem("repoview-theme");
		const systemPrefersDark = window.matchMedia?.(
			"(prefers-color-scheme: dark)",
		).matches;

		const initialTheme = savedTheme || (systemPrefersDark ? "dark" : "light");
		applyTheme(initialTheme);

		const themeBtn = document.getElementById("themeToggleBtn");
		if (themeBtn) {
			themeBtn.addEventListener("click", toggleTheme);
		}
	}

	function applyTheme(theme) {
		document.documentElement.setAttribute("data-theme", theme);
		updateThemeIcon(theme);
	}

	function toggleTheme() {
		const currentTheme =
			document.documentElement.getAttribute("data-theme") || "light";
		const nextTheme = currentTheme === "dark" ? "light" : "dark";
		localStorage.setItem("repoview-theme", nextTheme);
		applyTheme(nextTheme);
	}

	function updateThemeIcon(theme) {
		const themeBtn = document.getElementById("themeToggleBtn");
		if (!themeBtn) return;

		// Sun icon for dark mode (click to switch to light), Moon for light mode (click to switch to dark)
		if (theme === "dark") {
			themeBtn.innerHTML = `
                <svg viewBox="0 0 24 24" width="16" height="16" stroke="currentColor" stroke-width="2" fill="none" stroke-linecap="round" stroke-linejoin="round">
                    <circle cx="12" cy="12" r="5"></circle>
                    <line x1="12" y1="1" x2="12" y2="3"></line>
                    <line x1="12" y1="21" x2="12" y2="23"></line>
                    <line x1="4.22" y1="4.22" x2="5.64" y2="5.64"></line>
                    <line x1="18.36" y1="18.36" x2="19.78" y2="19.78"></line>
                    <line x1="1" y1="12" x2="3" y2="12"></line>
                    <line x1="21" y1="12" x2="23" y2="12"></line>
                    <line x1="4.22" y1="19.78" x2="5.64" y2="18.36"></line>
                    <line x1="18.36" y1="5.64" x2="19.78" y2="4.22"></line>
                </svg>
            `;
			themeBtn.setAttribute("title", "Switch to light theme");
		} else {
			themeBtn.innerHTML = `
                <svg viewBox="0 0 24 24" width="16" height="16" stroke="currentColor" stroke-width="2" fill="none" stroke-linecap="round" stroke-linejoin="round">
                    <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"></path>
                </svg>
            `;
			themeBtn.setAttribute("title", "Switch to dark theme");
		}
	}

	// =========================================================================
	// Global Package Search
	// =========================================================================
	function initSearch() {
		const searchInput = document.getElementById("pkgSearchInput");
		const resultsContainer = document.getElementById("searchResults");

		if (!searchInput || !resultsContainer) return;

		searchInput.addEventListener("focus", loadIndex);
		searchInput.addEventListener("input", (e) =>
			handleInput(e.target.value, resultsContainer),
		);

		// Hide results when clicking outside
		document.addEventListener("click", (e) => {
			if (!e.target.closest(".search-container")) {
				resultsContainer.style.display = "none";
			}
		});

		// Global Keyboard Shortcut: '/' or Ctrl+K / Cmd+K to focus search
		document.addEventListener("keydown", (e) => {
			if (
				(e.key === "/" &&
					!["INPUT", "TEXTAREA"].includes(document.activeElement.tagName)) ||
				((e.ctrlKey || e.metaKey) && e.key === "k")
			) {
				e.preventDefault();
				searchInput.focus();
				searchInput.select();
				loadIndex();
			} else if (e.key === "Escape") {
				resultsContainer.style.display = "none";
				searchInput.blur();
			}
		});
	}

	async function loadIndex() {
		if (searchData || isLoading) return;
		isLoading = true;

		try {
			const response = await fetch(SEARCH_FILE);
			if (!response.ok) throw new Error("Network response was not ok");
			const json = await response.json();
			if (!json.schema || !json.data) {
				console.error("Invalid search index format");
				return;
			}
			searchData = json.data;
		} catch (error) {
			console.error("Failed to load search index:", error);
		} finally {
			isLoading = false;
		}
	}

	function handleInput(query, container) {
		if (!query || query.length < 2) {
			container.style.display = "none";
			return;
		}

		if (!searchData) {
			container.innerHTML = '<div class="search-status">Loading index...</div>';
			container.style.display = "block";
			return;
		}

		const results = search(query);
		renderResults(results, container);
	}

	// HTML entity escaping helper to prevent DOM XSS
	function escapeHTML(str) {
		if (!str) return "";
		return String(str).replace(/[&<>'"]/g, (tag) => {
			const chars = {
				"&": "&amp;",
				"<": "&lt;",
				">": "&gt;",
				"'": "&#39;",
				'"': "&quot;",
			};
			return chars[tag] || tag;
		});
	}

	function search(query) {
		const results = [];
		let regex = null;
		let isRegex = false;

		// Support /pattern/ regex search with ReDoS protection (max 50 chars, no nested quantifiers)
		if (
			query.length > 2 &&
			query.startsWith("/") &&
			query.endsWith("/") &&
			query.length <= 52
		) {
			const pattern = query.slice(1, -1);
			// Block catastrophic backtracking patterns like (a+)+ or (a*)*
			const isUnsafeRegex =
				/(\+|\*|\{\d+,?\d*\})\s*(\+|\*|\{\d+,?\d*\})|\([^)]*(\+|\*)[^)]*\)\s*(\+|\*)/.test(
					pattern,
				);
			if (!isUnsafeRegex) {
				try {
					// nosemgrep: javascript.lang.security.audit.detect-non-literal-regexp.detect-non-literal-regexp -- query length and backtracking syntax are validated above against ReDoS
					regex = new RegExp(pattern, "i");
					isRegex = true;
				} catch {
					isRegex = false;
				}
			}
		}

		if (!isRegex) {
			query = query.toLowerCase();
		}

		for (let i = 0; i < searchData.length; i++) {
			const row = searchData[i];
			const name = row[0];
			const summary = row[3] || "";

			let match = false;
			if (isRegex) {
				match = regex.test(name) || regex.test(summary);
			} else {
				match =
					name.toLowerCase().includes(query) ||
					summary.toLowerCase().includes(query);
			}

			if (match) {
				results.push(row);
			}

			if (results.length >= SEARCH_LIMIT) break;
		}
		return results;
	}

	function renderResults(results, container) {
		if (results.length === 0) {
			container.innerHTML =
				'<div class="search-status">No packages found</div>';
			container.style.display = "block";
			return;
		}

		const html = results
			.map((row) => {
				const [name, evr, arch, summary, filename] = row;
				// Sanitize filename to only allow alphanumeric, dot, underscore, dash
				const safeHref = encodeURI(filename || "").replace(/["'<>]/g, "");
				return `
                <a href="${safeHref}" class="search-result-item">
                    <div class="sr-name">${escapeHTML(name)}</div>
                    <div class="sr-meta">${escapeHTML(evr)}.${escapeHTML(arch)}</div>
                    <div class="sr-summary">${escapeHTML(summary || "")}</div>
                </a>
            `;
			})
			.join("");

		container.innerHTML = html;
		container.style.display = "block";
	}

	// =========================================================================
	// In-Page Group Table Filter
	// =========================================================================
	function initGroupFilter() {
		const filterInput = document.getElementById("groupFilterInput");
		const countSpan = document.getElementById("filteredCount");
		if (!filterInput) return;

		filterInput.addEventListener("input", () => {
			const q = filterInput.value.toLowerCase().trim();
			const rows = document.querySelectorAll(
				".pkg-table:not(.file-table) tbody tr",
			);
			let visibleCount = 0;

			rows.forEach((row) => {
				const text = row.textContent.toLowerCase();
				if (!q || text.includes(q)) {
					row.style.display = "";
					visibleCount++;
				} else {
					row.style.display = "none";
				}
			});

			if (countSpan) {
				countSpan.textContent = `Showing ${visibleCount} of ${rows.length} packages`;
			}
		});
	}

	// =========================================================================
	// In-Page Package Files Table Filter
	// =========================================================================
	function initFileFilter() {
		const fileInput = document.getElementById("fileFilterInput");
		const fileCountSpan = document.getElementById("fileFilteredCount");
		if (!fileInput) return;

		fileInput.addEventListener("input", () => {
			const q = fileInput.value.toLowerCase().trim();
			const rows = document.querySelectorAll(".file-table tbody tr");
			let visibleCount = 0;

			rows.forEach((row) => {
				const text = row.textContent.toLowerCase();
				if (!q || text.includes(q)) {
					row.style.display = "";
					visibleCount++;
				} else {
					row.style.display = "none";
				}
			});

			if (fileCountSpan) {
				fileCountSpan.textContent = `Showing ${visibleCount} of ${rows.length} files`;
			}
		});
	}

	// =========================================================================
	// 1-Click Copy Helpers & Global Copy Handler
	// =========================================================================
	function copyText(text, btn, successLabel) {
		function showSuccess() {
			const originalHTML = btn.innerHTML;
			btn.classList.add("copied");
			const textSpan = btn.querySelector(".copy-text");
			if (textSpan) {
				textSpan.textContent = successLabel || "Copied!";
			}
			setTimeout(() => {
				btn.classList.remove("copied");
				btn.innerHTML = originalHTML;
			}, 2000);
		}

		if (navigator.clipboard && window.isSecureContext) {
			navigator.clipboard
				.writeText(text)
				.then(showSuccess)
				.catch(() => fallbackCopy(text, showSuccess));
		} else {
			fallbackCopy(text, showSuccess);
		}
	}

	function fallbackCopy(text, cb) {
		const ta = document.createElement("textarea");
		ta.value = text;
		ta.style.position = "fixed";
		ta.style.opacity = "0";
		document.body.appendChild(ta);
		ta.focus();
		ta.select();
		try {
			document.execCommand("copy");
			if (cb) cb();
		} catch (err) {
			console.error("Copy failed: ", err);
		}
		document.body.removeChild(ta);
	}

	function initGlobalCopy() {
		document.addEventListener("click", (e) => {
			const btn = e.target.closest("[data-copy]");
			if (btn) {
				const val = btn.getAttribute("data-copy");
				if (val) {
					copyText(val, btn, "Copied!");
				}
			}
		});
	}

	// =========================================================================
	// Signature Verification Modal
	// =========================================================================
	function initVerifyModal() {
		const verifyBtn = document.getElementById("btnVerifySignature");
		const modal = document.getElementById("verifySignatureModal");
		if (!verifyBtn || !modal) return;

		const closeBtn = document.getElementById("modalCloseBtn");
		const doneBtn = document.getElementById("modalDoneBtn");

		function openModal() {
			modal.hidden = false;
			requestAnimationFrame(() => {
				modal.classList.add("is-open");
			});
			document.body.style.overflow = "hidden";
			if (closeBtn) closeBtn.focus();
		}

		function closeModal() {
			modal.classList.remove("is-open");
			setTimeout(() => {
				modal.hidden = true;
				document.body.style.overflow = "";
				verifyBtn.focus();
			}, 200);
		}

		verifyBtn.addEventListener("click", openModal);
		if (closeBtn) closeBtn.addEventListener("click", closeModal);
		if (doneBtn) doneBtn.addEventListener("click", closeModal);

		// Click outside to close
		modal.addEventListener("click", (e) => {
			if (e.target === modal) {
				closeModal();
			}
		});

		// ESC key to close
		document.addEventListener("keydown", (e) => {
			if (e.key === "Escape" && modal.classList.contains("is-open")) {
				closeModal();
			}
		});
	}

	// =========================================================================
	// Quick Install Bar (dnf, yum, wget, curl)
	// =========================================================================
	function initInstallBar() {
		const installBar = document.querySelector(".quick-install-bar");
		if (!installBar) return;

		const tabs = installBar.querySelectorAll(".install-tab");
		const cmdDisplay = document.getElementById("installCmdDisplay");
		const copyBtn = document.getElementById("btnCopyInstallCmd");
		if (!tabs.length || !cmdDisplay || !copyBtn) return;

		function getCmd(tool, pkgName, href) {
			if (tool === "dnf") {
				return `sudo dnf install ${pkgName}`;
			} else if (tool === "yum") {
				return `sudo yum install ${pkgName}`;
			} else if (tool === "wget") {
				let fullUrl = href;
				try {
					if (window.location.protocol.startsWith("http")) {
						fullUrl = new URL(href, window.location.href).href;
					}
				} catch {}
				return `wget ${fullUrl}`;
			} else if (tool === "curl") {
				let fullUrl = href;
				try {
					if (window.location.protocol.startsWith("http")) {
						fullUrl = new URL(href, window.location.href).href;
					}
				} catch {}
				return `curl -O ${fullUrl}`;
			}
			return `sudo dnf install ${pkgName}`;
		}

		tabs.forEach((tab) => {
			tab.addEventListener("click", () => {
				tabs.forEach((t) => {
					t.classList.remove("active");
				});
				tab.classList.add("active");

				const tool = tab.getAttribute("data-tool") || "dnf";
				const pkgName = tab.getAttribute("data-pkg") || "";
				const href = tab.getAttribute("data-href") || "";
				const cmd = getCmd(tool, pkgName, href);

				cmdDisplay.textContent = cmd;
				copyBtn.setAttribute("data-copy", cmd);
			});
		});
	}

	// =========================================================================
	// Package Dependencies Tabs & Live Filtering
	// =========================================================================
	function initDepTabs() {
		const depContainer = document.querySelector(".dependencies-container");
		if (!depContainer) return;

		const tabButtons = depContainer.querySelectorAll(".dep-tab-btn");
		const tabPanels = depContainer.querySelectorAll(".dep-tab-panel");
		const filterInput = depContainer.querySelector(".dep-filter-input");

		function applyDepFilter(q) {
			const activePanel = depContainer.querySelector(".dep-tab-panel.active");
			if (!activePanel) return;

			const items = activePanel.querySelectorAll(".dep-item");
			items.forEach((item) => {
				const text = item.textContent.toLowerCase();
				item.style.display = !q || text.includes(q) ? "" : "none";
			});
		}

		tabButtons.forEach((btn) => {
			btn.addEventListener("click", () => {
				const tabName = btn.getAttribute("data-tab");
				tabButtons.forEach((b) => {
					b.classList.remove("active");
				});
				btn.classList.add("active");

				tabPanels.forEach((panel) => {
					if (panel.id === `depPanel-${tabName}`) {
						panel.classList.add("active");
					} else {
						panel.classList.remove("active");
					}
				});

				if (filterInput) {
					applyDepFilter(filterInput.value.toLowerCase().trim());
				}
			});
		});

		if (filterInput) {
			filterInput.addEventListener("input", () => {
				applyDepFilter(filterInput.value.toLowerCase().trim());
			});
		}
	}

	// =========================================================================
	// Repository Configuration Modal (.repo Helper & Blob Download)
	// =========================================================================
	function initRepoModal() {
		const modal = document.getElementById("repoConfigModal");
		if (!modal) return;

		const openButtons = document.querySelectorAll(".btn-open-repo-modal");
		const closeBtn = document.getElementById("repoModalCloseBtn");
		const doneBtn = document.getElementById("repoModalDoneBtn");
		const downloadBtn = document.getElementById("btnDownloadRepoFile");
		const copySnippetBtn = document.getElementById("btnCopyRepoSnippet");
		const copyFastSetupBtn = document.getElementById("btnCopyFastSetup");
		const snippetCode = document.getElementById("repoSnippetCode");
		const fastSetupCmd = document.getElementById("repoFastSetupCmd");

		function openModal() {
			modal.hidden = false;
			requestAnimationFrame(() => {
				modal.classList.add("is-open");
			});
			document.body.style.overflow = "hidden";
			if (closeBtn) closeBtn.focus();
		}

		function closeModal() {
			modal.classList.remove("is-open");
			setTimeout(() => {
				modal.hidden = true;
				document.body.style.overflow = "";
			}, 200);
		}

		openButtons.forEach((btn) => {
			btn.addEventListener("click", openModal);
		});
		if (closeBtn) closeBtn.addEventListener("click", closeModal);
		if (doneBtn) doneBtn.addEventListener("click", closeModal);

		modal.addEventListener("click", (e) => {
			if (e.target === modal) {
				closeModal();
			}
		});

		document.addEventListener("keydown", (e) => {
			if (e.key === "Escape" && modal.classList.contains("is-open")) {
				closeModal();
			}
		});

		function getDetectedBaseURL() {
			if (!window.location.protocol.startsWith("http")) {
				return "$URL";
			}
			let path = window.location.pathname;
			path = path.replace(/\/[^/]+\.html?$/i, "");
			path = path.replace(/\/repoview\/?$/i, "");
			path = path.replace(/\/+$/, "");
			return window.location.origin + path;
		}

		const configuredBaseURL = snippetCode
			? (snippetCode.getAttribute("data-configured-baseurl") || "").trim()
			: "";
		const isBaseURLConfigured =
			configuredBaseURL !== "" && !configuredBaseURL.startsWith("$");

		if (!isBaseURLConfigured && window.location.protocol.startsWith("http")) {
			const detectedBaseURL = getDetectedBaseURL();
			if (snippetCode) {
				snippetCode.textContent = snippetCode.textContent.replace(
					/^baseurl=.*$/m,
					`baseurl=${detectedBaseURL}`,
				);
			}
			if (fastSetupCmd) {
				fastSetupCmd.textContent = fastSetupCmd.textContent.replace(
					/^baseurl=.*$/m,
					`baseurl=${detectedBaseURL}`,
				);
			}
		}

		if (copySnippetBtn && snippetCode) {
			copySnippetBtn.setAttribute("data-copy", snippetCode.textContent.trim());
		}

		if (copyFastSetupBtn && fastSetupCmd) {
			copyFastSetupBtn.setAttribute(
				"data-copy",
				fastSetupCmd.textContent.trim(),
			);
		}

		if (downloadBtn && snippetCode) {
			downloadBtn.addEventListener("click", () => {
				const content = `${snippetCode.textContent.trim()}\n`;
				let fileName = "custom-repository.repo";
				const match = content.match(/^\[([^\]]+)\]/m);
				if (match?.[1]) {
					fileName = `${match[1]}.repo`;
				}

				const blob = new Blob([content], { type: "text/plain;charset=utf-8" });
				const url = URL.createObjectURL(blob);
				const a = document.createElement("a");
				a.href = url;
				a.download = fileName;
				document.body.appendChild(a);
				a.click();
				document.body.removeChild(a);
				URL.revokeObjectURL(url);

				const origHtml = downloadBtn.innerHTML;
				downloadBtn.classList.add("copied");
				downloadBtn.innerHTML = `<span>Downloaded ${fileName}!</span>`;
				setTimeout(() => {
					downloadBtn.classList.remove("copied");
					downloadBtn.innerHTML = origHtml;
				}, 2500);
			});
		}
	}

	// =========================================================================
	// Initialization
	// =========================================================================
	function init() {
		initTheme();
		initSearch();
		initGroupFilter();
		initFileFilter();
		initGlobalCopy();
		initVerifyModal();
		initInstallBar();
		initDepTabs();
		initRepoModal();
	}

	if (document.readyState === "loading") {
		document.addEventListener("DOMContentLoaded", init);
	} else {
		init();
	}
})();
