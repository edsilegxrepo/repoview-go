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

	function stripControlChars(val) {
		const s = String(val || "");
		let out = "";
		for (let i = 0; i < s.length; i++) {
			const code = s.charCodeAt(i);
			if (code >= 32 && code !== 127) {
				out += s[i];
			}
		}
		return out;
	}
	const SEARCH_LIMIT = 50;

	// State
	let searchData = null;
	let searchColMap = { n: 0, v: 1, a: 2, s: 3, f: 4, g: 5, l: 6, p: 7, t: 8 };
	let activeResultIndex = -1;
	let isLoading = false;

	// =========================================================================
	// Theme Management (Light / Dark)
	// =========================================================================
	function initTheme() {
		const savedTheme = localStorage.getItem("repoview-theme");
		const initialTheme = savedTheme || "dark";
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
			document.documentElement.getAttribute("data-theme") || "dark";
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
		searchInput.addEventListener("input", (e) => {
			activeResultIndex = -1;
			handleInput(e.target.value, resultsContainer);
		});

		searchInput.addEventListener("keydown", (e) => {
			const items = resultsContainer.querySelectorAll(".search-result-item");
			if (!items.length || resultsContainer.style.display === "none") return;
			if (e.key === "ArrowDown") {
				e.preventDefault();
				activeResultIndex = (activeResultIndex + 1) % items.length;
				updateActiveResult(items);
			} else if (e.key === "ArrowUp") {
				e.preventDefault();
				activeResultIndex =
					activeResultIndex <= 0 ? items.length - 1 : activeResultIndex - 1;
				updateActiveResult(items);
			} else if (e.key === "Enter" && activeResultIndex >= 0) {
				e.preventDefault();
				const href = items[activeResultIndex].getAttribute("href");
				if (href && href !== "#") {
					window.location.href = href;
				}
			}
		});

		function updateActiveResult(items) {
			items.forEach((el, idx) => {
				if (idx === activeResultIndex) {
					el.classList.add("active");
					el.scrollIntoView({ block: "nearest" });
				} else {
					el.classList.remove("active");
				}
			});
		}

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
			searchColMap = {
				n: -1,
				v: -1,
				a: -1,
				s: -1,
				f: -1,
				g: -1,
				l: -1,
				p: -1,
				t: -1,
			};
			json.schema.forEach((col, idx) => {
				searchColMap[col] = idx;
			});
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

	function parseQueryQualifiers(rawQuery) {
		const filters = {
			arch: "",
			group: "",
			license: "",
			provides: "",
			tag: "",
			cve: "",
		};
		const freeTokens = [];
		const parts = String(rawQuery || "")
			.trim()
			.split(/\s+/);
		for (const part of parts) {
			if (!part) continue;
			const colon = part.indexOf(":");
			if (colon > 0 && colon < part.length - 1) {
				const key = part.slice(0, colon).toLowerCase();
				const val = part.slice(colon + 1).toLowerCase();
				if (Object.hasOwn(filters, key)) {
					filters[key] = val;
					continue;
				}
			}
			freeTokens.push(part);
		}
		return { filters, text: freeTokens.join(" ") };
	}

	function search(rawQuery) {
		const { filters, text } = parseQueryQualifiers(rawQuery);
		const hasQualifiers = Object.values(filters).some((v) => v !== "");
		let regex = null;
		let isRegex = false;

		// Support /pattern/ regex search with ReDoS protection (max 50 chars, no nested quantifiers)
		if (
			text.length > 2 &&
			text.startsWith("/") &&
			text.endsWith("/") &&
			text.length <= 52
		) {
			const pattern = text.slice(1, -1);
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

		const queryLower = isRegex ? "" : text.toLowerCase();
		const scored = [];
		const col = (row, key) => {
			const i = searchColMap[key];
			return i >= 0 && i < row.length ? row[i] || "" : "";
		};

		for (let i = 0; i < searchData.length; i++) {
			const row = searchData[i];
			const name = col(row, "n");
			const arch = col(row, "a");
			const summary = col(row, "s");
			const group = col(row, "g");
			const license = col(row, "l");
			const provides = col(row, "p");
			const tags = col(row, "t");

			if (filters.arch && arch.toLowerCase() !== filters.arch) continue;
			if (filters.group && !group.toLowerCase().includes(filters.group))
				continue;
			if (filters.license && !license.toLowerCase().includes(filters.license))
				continue;
			if (
				filters.provides &&
				!provides.toLowerCase().includes(filters.provides)
			)
				continue;
			if (filters.tag && !tags.toLowerCase().includes(filters.tag)) continue;
			if (filters.cve && !tags.toLowerCase().includes(filters.cve)) continue;

			let score = hasQualifiers && text === "" ? 10 : 0;
			if (text !== "") {
				if (isRegex) {
					if (regex.test(name)) score = 60;
					else if (regex.test(summary)) score = 30;
					else if (regex.test(provides) || regex.test(tags)) score = 15;
				} else {
					const nameLower = name.toLowerCase();
					const summaryLower = summary.toLowerCase();
					const providesLower = provides.toLowerCase();
					const tagsLower = tags.toLowerCase();
					const groupLower = group.toLowerCase();
					const licenseLower = license.toLowerCase();

					if (nameLower === queryLower) score = 100;
					else if (nameLower.startsWith(queryLower)) score = 80;
					else if (nameLower.includes(queryLower)) score = 60;
					else if (summaryLower.includes(queryLower)) score = 40;
					else if (providesLower.includes(queryLower)) score = 25;
					else if (
						groupLower.includes(queryLower) ||
						licenseLower.includes(queryLower) ||
						tagsLower.includes(queryLower)
					) {
						score = 15;
					}
				}
			}

			if (score > 0) {
				scored.push({ row, score, name });
			}
		}

		scored.sort((a, b) => {
			if (b.score !== a.score) return b.score - a.score;
			return a.name.localeCompare(b.name);
		});
		return scored.slice(0, SEARCH_LIMIT).map((item) => item.row);
	}

	function renderResults(results, container) {
		if (results.length === 0) {
			container.innerHTML =
				'<div class="search-status">No packages found</div>';
			container.style.display = "block";
			return;
		}

		const col = (row, key) => {
			const i = searchColMap[key];
			return i >= 0 && i < row.length ? row[i] || "" : "";
		};

		const html = results
			.map((row) => {
				const name = col(row, "n");
				const evr = col(row, "v");
				const arch = col(row, "a");
				const summary = col(row, "s");
				const filename = col(row, "f");
				const group = col(row, "g");
				// Enforce strict relative HTML filename allowlist (no schemes, slashes, or traversal)
				const cleanFile = typeof filename === "string" ? filename.trim() : "";
				const safeHref =
					/^[A-Za-z0-9][A-Za-z0-9._+,@~-]*\.html$/.test(cleanFile) &&
					!cleanFile.includes("..")
						? encodeURIComponent(cleanFile)
						: "#";
				const groupMeta = group ? ` &bull; ${escapeHTML(group)}` : "";
				return `
                <a href="${safeHref}" class="search-result-item">
                    <div class="sr-name">${escapeHTML(name)}</div>
                    <div class="sr-meta">${escapeHTML(evr)}.${escapeHTML(arch)}${groupMeta}</div>
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

		function shellQuote(val) {
			const clean = stripControlChars(val).trim();
			if (clean === "") return "''";
			if (/^[A-Za-z0-9][A-Za-z0-9._:=+/@-]*$/.test(clean)) {
				return clean;
			}
			return `'${clean.replace(/'/g, "'\\''")}'`;
		}

		function getCmd(tool, pkgName, href) {
			const safePkg = shellQuote(pkgName);
			if (tool === "apt") {
				return `sudo apt install ${safePkg}`;
			} else if (tool === "dpkg") {
				const filename = href.split("/").pop() || `${pkgName}.deb`;
				return `sudo dpkg -i ${shellQuote(filename)}`;
			} else if (tool === "dnf") {
				return `sudo dnf install ${safePkg}`;
			} else if (tool === "yum") {
				return `sudo yum install ${safePkg}`;
			} else if (tool === "wget") {
				let fullUrl = href;
				try {
					if (window.location.protocol.startsWith("http")) {
						fullUrl = new URL(href, window.location.href).href;
					}
				} catch {}
				return `wget ${shellQuote(fullUrl)}`;
			} else if (tool === "curl") {
				let fullUrl = href;
				try {
					if (window.location.protocol.startsWith("http")) {
						fullUrl = new URL(href, window.location.href).href;
					}
				} catch {}
				return `curl -O ${shellQuote(fullUrl)}`;
			}
			return `sudo dnf install ${safePkg}`;
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
			const detectedBaseURL = stripControlChars(getDetectedBaseURL());
			if (snippetCode) {
				snippetCode.textContent = snippetCode.textContent
					.replace(/^baseurl=.*$/m, `baseurl=${detectedBaseURL}`)
					.replace(/^URIs: .*$/m, `URIs: ${detectedBaseURL}`);
			}
			if (fastSetupCmd) {
				fastSetupCmd.textContent = fastSetupCmd.textContent
					.replace(/^baseurl=.*$/m, `baseurl=${detectedBaseURL}`)
					.replace(/^URIs: .*$/m, `URIs: ${detectedBaseURL}`);
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
				} else if (content.includes("Types: deb")) {
					fileName = "repository.sources";
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
