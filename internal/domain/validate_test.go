package domain

import (
	"strings"
	"testing"
)

// ── IsDNSLabel ────────────────────────────────────────────────────────────────

func TestIsDNSLabel(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		// valid
		{"ab", true},
		{"my-app", true},
		{"hello-world", true},
		{"web", true},
		{"app123", true},
		{"a1", true},
		{"my-app-42", true},
		{strings.Repeat("a", 63), true},       // max 63 chars
		{"a" + strings.Repeat("b", 61) + "c", true}, // 63 chars with inner
		// invalid
		{"", false},
		{"a", false},                    // too short (only 1 char)
		{"A", false},                    // uppercase
		{"MyApp", false},                // uppercase
		{"-app", false},                 // starts with hyphen
		{"app-", false},                 // ends with hyphen
		{"123app", false},               // starts with digit
		{"my_app", false},               // underscore
		{"my app", false},               // space
		{strings.Repeat("a", 64), false}, // too long
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.input, func(t *testing.T) {
			if got := IsDNSLabel(tt.input); got != tt.want {
				t.Errorf("IsDNSLabel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ── ValidateAppName ───────────────────────────────────────────────────────────

func TestValidateAppName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errFrag string // non-empty: error message must contain this substring
	}{
		{name: "valid simple", input: "my-app"},
		{name: "valid short", input: "ab"},
		{name: "valid with digits", input: "app42"},
		{name: "valid max length", input: strings.Repeat("a", 62) + "b"},
		{name: "empty", input: "", wantErr: true, errFrag: "must not be empty"},
		{name: "single char", input: "a", wantErr: true, errFrag: "DNS label"},
		{name: "uppercase", input: "MyApp", wantErr: true, errFrag: "DNS label"},
		{name: "starts with digit", input: "1app", wantErr: true, errFrag: "DNS label"},
		{name: "starts with hyphen", input: "-app", wantErr: true, errFrag: "DNS label"},
		{name: "ends with hyphen", input: "app-", wantErr: true, errFrag: "DNS label"},
		{name: "contains underscore", input: "my_app", wantErr: true, errFrag: "DNS label"},
		{name: "too long", input: strings.Repeat("a", 64), wantErr: true, errFrag: "DNS label"},
		{name: "contains space", input: "my app", wantErr: true, errFrag: "DNS label"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAppName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateAppName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errFrag != "" && !strings.Contains(err.Error(), tt.errFrag) {
				t.Errorf("ValidateAppName(%q) error = %q, want substring %q", tt.input, err.Error(), tt.errFrag)
			}
		})
	}
}

// ── ValidateComponentName ─────────────────────────────────────────────────────

func TestValidateComponentName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errFrag string
	}{
		{name: "valid web", input: "web"},
		{name: "valid worker", input: "worker"},
		{name: "valid cron", input: "cron"},
		{name: "valid hyphenated", input: "my-worker"},
		{name: "valid alphanumeric", input: "worker2"},
		{name: "empty", input: "", wantErr: true, errFrag: "must not be empty"},
		{name: "single char", input: "w", wantErr: true, errFrag: "DNS label"},
		{name: "uppercase", input: "Web", wantErr: true, errFrag: "DNS label"},
		{name: "starts with digit", input: "2worker", wantErr: true, errFrag: "DNS label"},
		{name: "ends with hyphen", input: "worker-", wantErr: true, errFrag: "DNS label"},
		{name: "contains dot", input: "my.worker", wantErr: true, errFrag: "DNS label"},
		{name: "too long", input: strings.Repeat("w", 64), wantErr: true, errFrag: "DNS label"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateComponentName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateComponentName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errFrag != "" && !strings.Contains(err.Error(), tt.errFrag) {
				t.Errorf("ValidateComponentName(%q) error = %q, want substring %q", tt.input, err.Error(), tt.errFrag)
			}
		})
	}
}

// ── ValidateComponents ────────────────────────────────────────────────────────

func TestValidateComponents(t *testing.T) {
	web := ComponentSpec{Name: "web", Type: ComponentWeb, Enabled: true, PreviewEnabled: true}
	worker := ComponentSpec{Name: "worker", Type: ComponentWorker, Enabled: true}
	cron := ComponentSpec{Name: "cron", Type: ComponentCron, Enabled: true}

	tests := []struct {
		name       string
		components []ComponentSpec
		wantErr    bool
		errFrag    string
	}{
		{
			name:       "single web component",
			components: []ComponentSpec{web},
		},
		{
			name:       "web and worker",
			components: []ComponentSpec{web, worker},
		},
		{
			name:       "all three types",
			components: []ComponentSpec{web, worker, cron},
		},
		{
			name:       "empty list",
			components: []ComponentSpec{},
			wantErr:    true,
			errFrag:    "at least one component",
		},
		{
			name:       "nil list",
			components: nil,
			wantErr:    true,
			errFrag:    "at least one component",
		},
		{
			name:       "duplicate component name",
			components: []ComponentSpec{web, {Name: "web", Type: ComponentWorker}},
			wantErr:    true,
			errFrag:    "duplicate component name",
		},
		{
			name:       "invalid component name - empty",
			components: []ComponentSpec{{Name: "", Type: ComponentWeb}},
			wantErr:    true,
			errFrag:    "must not be empty",
		},
		{
			name:       "invalid component name - uppercase",
			components: []ComponentSpec{{Name: "Web", Type: ComponentWeb}},
			wantErr:    true,
			errFrag:    "DNS label",
		},
		{
			name:       "invalid component name - starts with digit",
			components: []ComponentSpec{{Name: "1web", Type: ComponentWeb}},
			wantErr:    true,
			errFrag:    "DNS label",
		},
		{
			name:       "unsupported component type",
			components: []ComponentSpec{{Name: "gateway", Type: "gateway"}},
			wantErr:    true,
			errFrag:    "unsupported type",
		},
		{
			name:       "unsupported component type uppercase",
			components: []ComponentSpec{{Name: "ws", Type: "WEB"}},
			wantErr:    true,
			errFrag:    "unsupported type",
		},
		{
			name:       "valid component names with hyphens",
			components: []ComponentSpec{{Name: "my-worker", Type: ComponentWorker}},
		},
		{
			name: "many components all valid",
			components: []ComponentSpec{
				{Name: "web", Type: ComponentWeb},
				{Name: "bg-worker", Type: ComponentWorker},
				{Name: "scheduled-job", Type: ComponentCron},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateComponents(tt.components)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateComponents() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errFrag != "" && !strings.Contains(err.Error(), tt.errFrag) {
				t.Errorf("ValidateComponents() error = %q, want substring %q", err.Error(), tt.errFrag)
			}
		})
	}
}

// ── ValidateSingleExposedComponent ───────────────────────────────────────────

func TestValidateSingleExposedComponent(t *testing.T) {
	tests := []struct {
		name          string
		components    []ComponentSpec
		allowMultiple bool
		wantErr       bool
		errFrag       string
	}{
		{
			name:          "one web component - allowed",
			components:    []ComponentSpec{{Name: "web", Type: ComponentWeb}},
			allowMultiple: false,
		},
		{
			name: "no web components - allowed",
			components: []ComponentSpec{
				{Name: "worker", Type: ComponentWorker},
				{Name: "cron", Type: ComponentCron},
			},
			allowMultiple: false,
		},
		{
			name: "two web components - not allowed",
			components: []ComponentSpec{
				{Name: "frontend", Type: ComponentWeb},
				{Name: "api", Type: ComponentWeb},
			},
			allowMultiple: false,
			wantErr:       true,
			errFrag:       "2 web components",
		},
		{
			name: "three web components - not allowed",
			components: []ComponentSpec{
				{Name: "aa", Type: ComponentWeb},
				{Name: "ab", Type: ComponentWeb},
				{Name: "ac", Type: ComponentWeb},
			},
			allowMultiple: false,
			wantErr:       true,
			errFrag:       "3 web components",
		},
		{
			name: "two web components - explicitly allowed",
			components: []ComponentSpec{
				{Name: "frontend", Type: ComponentWeb},
				{Name: "api", Type: ComponentWeb},
			},
			allowMultiple: true,
		},
		{
			name:          "empty list - no error",
			components:    []ComponentSpec{},
			allowMultiple: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSingleExposedComponent(tt.components, tt.allowMultiple)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSingleExposedComponent() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errFrag != "" && !strings.Contains(err.Error(), tt.errFrag) {
				t.Errorf("ValidateSingleExposedComponent() error = %q, want substring %q", err.Error(), tt.errFrag)
			}
		})
	}
}

// ── SanitizePreviewName ───────────────────────────────────────────────────────

func TestSanitizePreviewName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "simple branch", input: "feature/my-thing", want: "feature-my-thing"},
		{name: "pr number prefix", input: "PR-42", want: "pr-42"},
		{name: "lowercase passthrough", input: "pr-42", want: "pr-42"},
		{name: "numeric only", input: "123", want: "pr-123"},
		{name: "numeric with suffix", input: "42-fix", want: "pr-42-fix"},
		{name: "slashes and underscores", input: "feat/some_feature", want: "feat-some-feature"},
		{name: "leading digits after slashes", input: "/123/foo", want: "pr-123-foo"},
		{name: "all non-alphanumeric", input: "---///---", want: "preview"},
		{name: "empty string", input: "", want: "preview"},
		{name: "uppercase letters", input: "FEATURE-BRANCH", want: "feature-branch"},
		{name: "mixed case with slashes", input: "Fix/Issue-99", want: "fix-issue-99"},
		{name: "trailing non-alphanumeric", input: "my-branch/", want: "my-branch"},
		{name: "leading non-alphanumeric", input: "/my-branch", want: "my-branch"},
		{name: "multiple consecutive separators", input: "feat//--bar", want: "feat-bar"},
		{
			name:  "long name truncated at 63",
			input: strings.Repeat("a", 70),
			want:  strings.Repeat("a", 63),
		},
		{
			// 62 a's + "--extra" → sanitized: 62 a's + "-extra" (68 chars)
			// truncated to 63: 62 a's + "-" → trailing hyphen stripped → 62 a's
			name:  "long name truncated, trailing hyphen stripped",
			input: strings.Repeat("a", 62) + "--extra",
			want:  strings.Repeat("a", 62),
		},
		{name: "dot separator", input: "v1.2.3", want: "v1-2-3"},
		{name: "single valid letter", input: "a", want: "a"}, // short but sanitizer returns as-is; ValidatePreviewName will reject it
		{name: "valid two-char", input: "ab", want: "ab"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizePreviewName(tt.input)
			if got != tt.want {
				t.Errorf("SanitizePreviewName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// SanitizePreviewName produces output that, when valid per ValidatePreviewName,
// is always a DNS label. Test that the sanitizer+validator pipeline works for
// representative inputs.
func TestSanitizeAndValidatePreviewName(t *testing.T) {
	valid := []string{
		"feature/my-thing",
		"PR-42",
		"123",
		"feat/some_feature",
		"Fix/Issue-99",
		"v1.2.3",
	}
	for _, raw := range valid {
		sanitized := SanitizePreviewName(raw)
		if err := ValidatePreviewName(sanitized); err != nil {
			t.Errorf("SanitizePreviewName(%q) = %q which failed ValidatePreviewName: %v", raw, sanitized, err)
		}
	}
}

// ── ValidatePreviewName ───────────────────────────────────────────────────────

func TestValidatePreviewName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errFrag string
	}{
		{name: "valid", input: "pr-42"},
		{name: "valid simple", input: "my-preview"},
		{name: "valid with digits", input: "feature42"},
		{name: "empty", input: "", wantErr: true, errFrag: "must not be empty"},
		{name: "single char", input: "p", wantErr: true, errFrag: "DNS label"},
		{name: "uppercase", input: "PR-42", wantErr: true, errFrag: "DNS label"},
		{name: "starts with digit", input: "42-preview", wantErr: true, errFrag: "DNS label"},
		{name: "starts with hyphen", input: "-pr-42", wantErr: true, errFrag: "DNS label"},
		{name: "ends with hyphen", input: "pr-42-", wantErr: true, errFrag: "DNS label"},
		{name: "contains slash", input: "feature/foo", wantErr: true, errFrag: "DNS label"},
		{name: "too long", input: strings.Repeat("p", 64), wantErr: true, errFrag: "DNS label"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePreviewName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePreviewName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errFrag != "" && !strings.Contains(err.Error(), tt.errFrag) {
				t.Errorf("ValidatePreviewName(%q) error = %q, want substring %q", tt.input, err.Error(), tt.errFrag)
			}
		})
	}
}

// ── ParseSizePreset ───────────────────────────────────────────────────────────

func TestParseSizePreset(t *testing.T) {
	tests := []struct {
		input   string
		want    SizePreset
		wantErr bool
	}{
		{input: "small", want: SizeSmall},
		{input: "medium", want: SizeMedium},
		{input: "large", want: SizeLarge},
		{input: "", wantErr: true},
		{input: "xl", wantErr: true},
		{input: "SMALL", wantErr: true},
		{input: "tiny", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseSizePreset(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSizePreset(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParseSizePreset(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSizePresetValid(t *testing.T) {
	tests := []struct {
		input SizePreset
		want  bool
	}{
		{SizeSmall, true},
		{SizeMedium, true},
		{SizeLarge, true},
		{"", false},
		{"XL", false},
		{"SMALL", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(string(tt.input), func(t *testing.T) {
			if got := tt.input.Valid(); got != tt.want {
				t.Errorf("SizePreset(%q).Valid() = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ── ValidateComponentSpec ─────────────────────────────────────────────────────

func TestValidateComponentSpec(t *testing.T) {
	tests := []struct {
		name    string
		input   ComponentSpec
		wantErr bool
		errFrag string
	}{
		{
			name:  "valid web enabled exposed",
			input: ComponentSpec{Name: "web", Type: ComponentWeb, Enabled: true, Expose: true, PreviewEnabled: true},
		},
		{
			name:  "valid worker with replicas",
			input: ComponentSpec{Name: "worker", Type: ComponentWorker, Enabled: true, Replicas: 3},
		},
		{
			name:  "valid cron with size preset",
			input: ComponentSpec{Name: "cron", Type: ComponentCron, Enabled: true, SizePreset: SizeSmall},
		},
		{
			name:  "valid with config map",
			input: ComponentSpec{Name: "web", Type: ComponentWeb, Enabled: true, Config: map[string]string{"KEY": "value"}},
		},
		{
			name:    "invalid name - empty",
			input:   ComponentSpec{Name: "", Type: ComponentWeb},
			wantErr: true,
			errFrag: "must not be empty",
		},
		{
			name:    "invalid name - uppercase",
			input:   ComponentSpec{Name: "Web", Type: ComponentWeb},
			wantErr: true,
			errFrag: "DNS label",
		},
		{
			name:    "invalid type",
			input:   ComponentSpec{Name: "svc", Type: "gateway"},
			wantErr: true,
			errFrag: "unsupported type",
		},
		{
			name:    "negative replicas",
			input:   ComponentSpec{Name: "web", Type: ComponentWeb, Replicas: -1},
			wantErr: true,
			errFrag: "replicas must be non-negative",
		},
		{
			name:    "invalid size preset",
			input:   ComponentSpec{Name: "web", Type: ComponentWeb, SizePreset: "xl"},
			wantErr: true,
			errFrag: "unknown size preset",
		},
		{
			name:    "replicas and sizePreset both set",
			input:   ComponentSpec{Name: "web", Type: ComponentWeb, Replicas: 2, SizePreset: SizeMedium},
			wantErr: true,
			errFrag: "mutually exclusive",
		},
		{
			name:  "zero replicas with size preset is valid",
			input: ComponentSpec{Name: "web", Type: ComponentWeb, SizePreset: SizeLarge},
		},
		{
			name:  "replicas with no size preset is valid",
			input: ComponentSpec{Name: "web", Type: ComponentWeb, Replicas: 5},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateComponentSpec(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateComponentSpec() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errFrag != "" && !strings.Contains(err.Error(), tt.errFrag) {
				t.Errorf("ValidateComponentSpec() error = %q, want substring %q", err.Error(), tt.errFrag)
			}
		})
	}
}

// ── AppPreviewNamespace ───────────────────────────────────────────────────────

func TestAppPreviewNamespace(t *testing.T) {
	tests := []struct {
		appName     string
		previewName string
		want        string
	}{
		{"hello", "pr-42", "hello-pr-42"},
		{"my-app", "feature-branch", "my-app-feature-branch"},
		{"api", "pr-182", "api-pr-182"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.appName+"/"+tt.previewName, func(t *testing.T) {
			got := AppPreviewNamespace(tt.appName, tt.previewName)
			if got != tt.want {
				t.Errorf("AppPreviewNamespace(%q, %q) = %q, want %q", tt.appName, tt.previewName, got, tt.want)
			}
		})
	}
}

// ── SanitizeAppName ───────────────────────────────────────────────────────────

func TestSanitizeAppName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "simple lowercase", input: "my-app", want: "my-app"},
		{name: "uppercase letters", input: "MyApp", want: "myapp"},
		{name: "slashes and underscores", input: "my/app_name", want: "my-app-name"},
		{name: "starts with digit", input: "42app", want: "app-42app"},
		{name: "numeric only", input: "123", want: "app-123"},
		{name: "all non-alphanumeric", input: "---///---", want: "app"},
		{name: "empty string", input: "", want: "app"},
		{name: "leading non-alphanumeric", input: "/myapp", want: "myapp"},
		{name: "trailing non-alphanumeric", input: "myapp/", want: "myapp"},
		{name: "multiple consecutive separators", input: "my//--app", want: "my-app"},
		{name: "dot separator", input: "v1.2.3", want: "v1-2-3"},
		{name: "mixed case with hyphens", input: "My-App", want: "my-app"},
		{name: "valid two-char", input: "ab", want: "ab"},
		{
			name:  "long name truncated at 63",
			input: strings.Repeat("a", 70),
			want:  strings.Repeat("a", 63),
		},
		{
			name:  "long name truncated, trailing hyphen stripped",
			input: strings.Repeat("a", 62) + "--extra",
			want:  strings.Repeat("a", 62),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeAppName(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeAppName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// SanitizeAppName produces output that, when valid per ValidateAppName, is
// always a DNS label. Test the sanitizer+validator pipeline for representative inputs.
func TestSanitizeAndValidateAppName(t *testing.T) {
	valid := []string{
		"MyApp",
		"my/app_name",
		"v1.2.3",
		"My-App",
		"hello-world",
	}
	for _, raw := range valid {
		sanitized := SanitizeAppName(raw)
		if err := ValidateAppName(sanitized); err != nil {
			t.Errorf("SanitizeAppName(%q) = %q which failed ValidateAppName: %v", raw, sanitized, err)
		}
	}
}

func TestSanitizeAppNameIsDeterministic(t *testing.T) {
	inputs := []string{"MyApp", "my/app_name", "42app", ""}
	for _, input := range inputs {
		first := SanitizeAppName(input)
		for i := 0; i < 3; i++ {
			got := SanitizeAppName(input)
			if got != first {
				t.Errorf("SanitizeAppName(%q) not deterministic: run %d got %q, first got %q", input, i, got, first)
			}
		}
	}
}

func TestValidateExposeModes(t *testing.T) {
	internalProfile := RoutingProfile{IngressClassName: "nginx-internal"}
	externalProfile := RoutingProfile{IngressClassName: "nginx", ClusterIssuer: "letsencrypt-prod"}
	orgWithBoth := RoutingProfiles{
		string(ExposeInternal): internalProfile,
		string(ExposeExternal): externalProfile,
	}
	orgInternalOnly := RoutingProfiles{
		string(ExposeInternal): internalProfile,
	}

	tests := []struct {
		name       string
		components []ComponentSpec
		org        RoutingProfiles
		env        RoutingProfiles
		wantErr    string // substring; "" = no error
	}{
		{
			name: "single external resolves",
			components: []ComponentSpec{
				{Name: "web", Type: ComponentWeb, Enabled: true, ExposeMode: ExposeExternal},
			},
			org: orgWithBoth,
		},
		{
			name: "single disabled is OK",
			components: []ComponentSpec{
				{Name: "worker", Type: ComponentWorker, Enabled: true, ExposeMode: ExposeDisabled},
			},
			org: orgWithBoth,
		},
		{
			name: "external + worker disabled passes",
			components: []ComponentSpec{
				{Name: "web", Type: ComponentWeb, Enabled: true, ExposeMode: ExposeExternal},
				{Name: "worker", Type: ComponentWorker, Enabled: true, ExposeMode: ExposeDisabled},
			},
			org: orgWithBoth,
		},
		{
			name: "two non-disabled rejected",
			components: []ComponentSpec{
				{Name: "admin", Type: ComponentWeb, Enabled: true, ExposeMode: ExposeInternal},
				{Name: "api", Type: ComponentWeb, Enabled: true, ExposeMode: ExposeExternal},
			},
			org:     orgWithBoth,
			wantErr: "at most one is allowed",
		},
		{
			name: "external mode without profile errors",
			components: []ComponentSpec{
				{Name: "web", Type: ComponentWeb, Enabled: true, ExposeMode: ExposeExternal},
			},
			org:     orgInternalOnly,
			wantErr: "no profile named \"external\"",
		},
		{
			name: "env override unblocks resolution",
			components: []ComponentSpec{
				{Name: "web", Type: ComponentWeb, Enabled: true, ExposeMode: ExposeExternal},
			},
			org: orgInternalOnly,
			env: RoutingProfiles{string(ExposeExternal): externalProfile},
		},
		{
			name: "no profiles configured: skip lookup (legacy compat)",
			components: []ComponentSpec{
				{Name: "web", Type: ComponentWeb, Enabled: true, ExposeMode: ExposeExternal},
			},
			// no orgProfiles, no envProfiles — legacy fall-through
		},
		{
			name: "legacy Expose=true counted as external",
			components: []ComponentSpec{
				{Name: "web", Type: ComponentWeb, Enabled: true, Expose: true},
				{Name: "api", Type: ComponentWeb, Enabled: true, Expose: true},
			},
			org:     orgWithBoth,
			wantErr: "at most one is allowed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateExposeModes(tt.components, tt.org, tt.env)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q missing substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}
