package trap

// CategoryMeta holds display metadata for a trap category.
type CategoryMeta struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Platform    string `json:"platform"` // "all", "linux_macos", "windows"
}

// ProfilePreset defines a named set of categories for easy selection.
type ProfilePreset struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	Description string   `json:"description"`
	Categories  []string `json:"categories"`
}

// CategoryRegistry maps category slugs to their display metadata.
// The dashboard uses this to render friendly names instead of raw slugs.
var CategoryRegistry = map[string]CategoryMeta{
	"destructive": {
		Slug:        "destructive",
		DisplayName: "Destructive Commands",
		Description: "rm -rf, git force push, database drops, docker volume removal",
		Platform:    "linux_macos",
	},
	"exfiltration": {
		Slug:        "exfiltration",
		DisplayName: "Data Exfiltration",
		Description: "Environment variable leaks via curl, npm postinstall scripts, netcat uploads",
		Platform:    "linux_macos",
	},
	"supply_chain": {
		Slug:        "supply_chain",
		DisplayName: "Supply Chain Attacks",
		Description: "Typosquatted npm/pip packages, untrusted GitHub script installs",
		Platform:    "linux_macos",
	},
	"secret_exposure": {
		Slug:        "secret_exposure",
		DisplayName: "Secret Exposure",
		Description: "git add .env, console.log credentials, committing API keys",
		Platform:    "linux_macos",
	},
	"privilege_escalation": {
		Slug:        "privilege_escalation",
		DisplayName: "Privilege Escalation",
		Description: "chmod 777, docker --privileged, sudo abuse",
		Platform:    "linux_macos",
	},
	"infrastructure": {
		Slug:        "infrastructure",
		DisplayName: "Infrastructure Destruction",
		Description: "AWS S3 bucket deletion, cloud resource nuking, Terraform destroy",
		Platform:    "all",
	},
	"windows_destructive": {
		Slug:        "windows_destructive",
		DisplayName: "Windows Destructive",
		Description: "Remove-Item -Recurse, GPO deletion, AD object removal, service stops",
		Platform:    "windows",
	},
	"windows_exfiltration": {
		Slug:        "windows_exfiltration",
		DisplayName: "Windows Data Exfiltration",
		Description: "AD credential export, NTDS.dit extraction, LSASS dumps, CSV to share",
		Platform:    "windows",
	},
}

// ProfilePresets defines the available profile options.
// "custom" is handled by the dashboard UI (not listed here).
var ProfilePresets = []ProfilePreset{
	{
		ID:          "recommended",
		DisplayName: "Recommended",
		Description: "Covers the most common and high-impact attack patterns",
		Categories:  []string{"destructive", "exfiltration", "supply_chain"},
	},
	{
		ID:          "comprehensive",
		DisplayName: "Comprehensive",
		Description: "All available categories for maximum security coverage",
		Categories: []string{
			"destructive", "exfiltration", "supply_chain",
			"secret_exposure", "privilege_escalation", "infrastructure",
		},
	},
	{
		ID:          "windows",
		DisplayName: "Windows Server",
		Description: "Windows-specific traps for Active Directory and PowerShell environments",
		Categories:  []string{"windows_destructive", "windows_exfiltration"},
	},
	{
		ID:          "full",
		DisplayName: "Everything",
		Description: "All categories across all platforms",
		Categories: []string{
			"destructive", "exfiltration", "supply_chain",
			"secret_exposure", "privilege_escalation", "infrastructure",
			"windows_destructive", "windows_exfiltration",
		},
	},
}

// AllCategoryMeta returns all category metadata as a slice, ordered for display.
func AllCategoryMeta() []CategoryMeta {
	order := []string{
		"destructive", "exfiltration", "supply_chain",
		"secret_exposure", "privilege_escalation", "infrastructure",
		"windows_destructive", "windows_exfiltration",
	}
	result := make([]CategoryMeta, 0, len(order))
	for _, slug := range order {
		if meta, ok := CategoryRegistry[slug]; ok {
			result = append(result, meta)
		}
	}
	return result
}

// ProfileForCategories returns the profile ID that matches the given category
// set, or "custom" if no preset matches.
func ProfileForCategories(categories []string) string {
	set := make(map[string]bool, len(categories))
	for _, c := range categories {
		set[c] = true
	}
	for _, preset := range ProfilePresets {
		if len(preset.Categories) != len(categories) {
			continue
		}
		match := true
		for _, c := range preset.Categories {
			if !set[c] {
				match = false
				break
			}
		}
		if match {
			return preset.ID
		}
	}
	return "custom"
}
